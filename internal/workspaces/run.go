package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type workspaceRun struct {
	result       Run
	repositoryID string
	request      string
	cancel       context.CancelFunc
	done         chan struct{}
	logs         *runLog
	processes    []*managedProcess
}

func (engine *Engine) Run(ctx context.Context, request RunRequest) (Run, error) {
	request = copyJSON(request)
	if request.RequestID == "" || len(request.RequestID) > 256 {
		return Run{}, errors.New("a request ID is required")
	}
	repository, path, err := engine.repository(ctx, request.Path, false)
	if err != nil {
		return Run{}, err
	}
	request.Path = path
	payload, _ := json.Marshal(request)
	unlock := engine.lock(path)
	defer unlock()
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return Run{}, errors.New("Switchyard is shutting down")
	}
	if existing := engine.requests[request.RequestID]; existing != nil {
		if existing.request != string(payload) {
			return Run{}, errors.New("this request ID was already used for a different run")
		}
		return copyJSON(existing.result), nil
	}
	if existing := engine.runs[path]; existing != nil && activeState(existing.result.State) {
		return Run{}, errors.New("this workspace is already running; stop it before running again")
	}
	choices, savedChoices := engine.config.Choices[path]
	if request.Target == "" {
		request.Target = choices.Target
	}
	if request.Target == "" {
		request.Target = repository.DefaultTarget
	}
	var target Target
	for _, configured := range repository.Targets {
		if configured.ID == request.Target {
			target = configured
			break
		}
	}
	if target.ID == "" {
		return Run{}, errors.New("the selected target is not configured")
	}
	if !request.PrepareOnly && target.Confirm && request.ConfirmedTarget != target.ID {
		return Run{}, fmt.Errorf("confirm target %q before running it", target.Name)
	}
	if request.Services == nil {
		if savedChoices {
			request.Services = append([]string{}, choices.Services...)
		} else {
			for _, service := range repository.Services {
				if service.Kind == "web" {
					request.Services = []string{service.ID}
					break
				}
			}
			if request.Services == nil && len(repository.Services) > 0 {
				request.Services = []string{repository.Services[0].ID}
			}
		}
	}
	services, err := selectedServices(repository, request.Services)
	if err != nil {
		return Run{}, err
	}
	if !request.PrepareOnly && len(services) == 0 {
		return Run{}, errors.New("select at least one service")
	}
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	config := copyJSON(engine.config)
	if config.Choices == nil {
		config.Choices = make(map[string]Choices)
	}
	config.Choices[path] = Choices{Target: request.Target, Services: append([]string(nil), request.Services...)}
	if err := SaveConfig(engine.configPath, config); err != nil {
		return Run{}, err
	}
	engine.config = config
	runContext, cancel := context.WithCancel(context.Background())
	run := &workspaceRun{result: Run{ID: newID(), Path: path, Target: request.Target, Requested: request.Services,
		State: "starting", Step: "Preparing workspace", StartedAt: time.Now().UTC(), Services: []ServiceRun{}, URLs: map[string]string{}},
		repositoryID: repository.ID, request: string(payload), cancel: cancel, done: make(chan struct{}), logs: &runLog{}}
	engine.runs[path] = run
	engine.requests[request.RequestID] = run
	engine.requestOrder = append(engine.requestOrder, request.RequestID)
	if len(engine.requestOrder) > 512 {
		id := engine.requestOrder[0]
		engine.requestOrder = engine.requestOrder[1:]
		if old := engine.requests[id]; old != nil && !activeState(old.result.State) {
			delete(engine.requests, id)
		}
	}
	engine.revision++
	go engine.execute(runContext, run, repository, target, services, request.PrepareOnly)
	return copyJSON(run.result), nil
}

func (engine *Engine) Stop(ctx context.Context, path string) (Run, error) {
	path, err := physicalWorkspacePath(path)
	if err != nil {
		return Run{}, err
	}
	unlock := engine.lock(path)
	defer unlock()
	engine.mu.Lock()
	run := engine.runs[path]
	if run == nil {
		engine.mu.Unlock()
		return Run{}, errors.New("this workspace has no running services")
	}
	if activeState(run.result.State) {
		run.result.State, run.result.Step = "stopping", "Stopping"
		engine.revision++
		run.cancel()
	}
	engine.mu.Unlock()
	select {
	case <-run.done:
	case <-ctx.Done():
		return Run{}, ctx.Err()
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return copyJSON(run.result), nil
}

func (engine *Engine) execute(ctx context.Context, run *workspaceRun, repository RepositoryConfig, target Target, services []ServiceConfig, prepareOnly bool) {
	defer close(run.done)
	defer run.cancel()
	state, message := "stopped", ""
	defer func() {
		for index := len(run.processes) - 1; index >= 0; index-- {
			run.processes[index].stop()
		}
		engine.ports.release(run.result.ID)
		engine.mu.Lock()
		defer engine.mu.Unlock()
		run.result.State, run.result.Error = state, message
		if state == "stopped" && prepareOnly && ctx.Err() == nil {
			run.result.Step = "Prepared"
		} else if state == "stopped" {
			run.result.Step = "Stopped"
		}
		for index := range run.result.Services {
			run.result.Services[index].State = "stopped"
			run.result.Services[index].PID = 0
		}
		run.result.URLs = map[string]string{}
		engine.revision++
	}()
	fail := func(err error) {
		if ctx.Err() == nil {
			state, message = "failed", err.Error()
			_, _ = fmt.Fprintln(run.logs, "Error:", message)
		}
	}
	ports := make(map[string]int)
	for _, service := range services {
		for _, port := range service.Ports {
			value, err := engine.ports.reserve(run.result.ID, port.Preferred)
			if err != nil {
				fail(err)
				return
			}
			ports[service.ID+"."+port.Name] = value
		}
	}
	for index, command := range repository.Setup {
		step := fmt.Sprintf("Setup %d of %d", index+1, len(repository.Setup))
		engine.step(run, step)
		if err := engine.finite(ctx, run, repository, target, command, ports, nil); err != nil {
			fail(fmt.Errorf("%s failed. %w", step, err))
			return
		}
	}
	if prepareOnly {
		return
	}
	for _, service := range services {
		portEnvironment := map[string]string{}
		for _, port := range service.Ports {
			if port.Environment != "" {
				portEnvironment[port.Environment] = strconv.Itoa(ports[service.ID+"."+port.Name])
			}
		}
		for _, command := range service.Prepare {
			engine.step(run, "Preparing "+service.Name)
			if err := engine.finite(ctx, run, repository, target, command, ports, portEnvironment); err != nil {
				fail(fmt.Errorf("Preparing %s failed. %w", service.Name, err))
				return
			}
		}
	}
	for _, service := range services {
		engine.step(run, "Starting "+service.Name)
		portEnvironment := map[string]string{}
		for _, port := range service.Ports {
			if port.Environment != "" {
				portEnvironment[port.Environment] = strconv.Itoa(ports[service.ID+"."+port.Name])
			}
		}
		engine.mu.Lock()
		runState := copyJSON(run.result)
		engine.mu.Unlock()
		spec, err := commandSpec(service.Command, repository, target, runState, ports, portEnvironment)
		if err != nil {
			fail(fmt.Errorf("%s: %w", service.Name, err))
			return
		}
		run.logs.remember(spec.Script, spec.Environment)
		process, err := launchProcess(ctx, spec, run.logs)
		if err != nil {
			fail(fmt.Errorf("%s: %w", service.Name, err))
			return
		}
		run.processes = append(run.processes, process)
		engine.mu.Lock()
		run.result.Services = append(run.result.Services, ServiceRun{ID: service.ID, Name: service.Name, State: "starting", PID: process.command.Process.Pid})
		engine.revision++
		engine.mu.Unlock()
		checkServices := func() error {
			for index, launched := range run.processes {
				if launched.exited() {
					return fmt.Errorf("%s exited during startup. Open the logs for its startup error.", services[index].Name)
				}
			}
			return nil
		}
		if err := waitReady(ctx, process, service.Readiness, ports[service.ID+"."+service.Readiness.Port], checkServices); err != nil {
			fail(fmt.Errorf("%s: %w", service.Name, err))
			return
		}
		engine.mu.Lock()
		run.result.Services[len(run.result.Services)-1].State = "running"
		for _, port := range service.Ports {
			if port.URL {
				run.result.URLs[service.ID+"."+port.Name] = "http://localhost:" + strconv.Itoa(ports[service.ID+"."+port.Name])
			}
		}
		engine.revision++
		engine.mu.Unlock()
	}
	engine.mu.Lock()
	run.result.State, run.result.Step = "running", "Running"
	engine.revision++
	engine.mu.Unlock()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	nextHealth := time.Now().Add(2 * time.Second)
	unhealthySince := make([]time.Time, len(services))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for index, process := range run.processes {
				if process.exited() {
					engine.mu.Lock()
					service := run.result.Services[index]
					engine.mu.Unlock()
					fail(fmt.Errorf("%s exited. Open the logs, then run again.", service.Name))
					return
				}
			}
			if time.Now().Before(nextHealth) {
				continue
			}
			nextHealth = time.Now().Add(2 * time.Second)
			for index, service := range services {
				ready, _ := probeReady(ctx, service.Readiness, ports[service.ID+"."+service.Readiness.Port])
				if ready {
					unhealthySince[index] = time.Time{}
				} else if unhealthySince[index].IsZero() {
					unhealthySince[index] = time.Now()
				}
				seconds := service.Readiness.TimeoutSeconds
				if seconds <= 0 {
					seconds = 60
				}
				if !unhealthySince[index].IsZero() && time.Since(unhealthySince[index]) >= time.Duration(seconds)*time.Second {
					fail(fmt.Errorf("%s stopped responding to its readiness check. Open the logs, then run again.", service.Name))
					return
				}
			}
			engine.mu.Lock()
			run.result.State, run.result.Step = "running", "Running"
			for index, unavailable := range unhealthySince {
				run.result.Services[index].State = "running"
				if !unavailable.IsZero() {
					run.result.Services[index].State = "starting"
					run.result.State, run.result.Step = "starting", "Waiting for "+services[index].Name+" to respond"
				}
			}
			engine.revision++
			engine.mu.Unlock()
		}
	}
}

func (engine *Engine) step(run *workspaceRun, step string) {
	engine.mu.Lock()
	run.result.Step = step
	engine.revision++
	engine.mu.Unlock()
	_, _ = fmt.Fprintln(run.logs, "\n"+step)
}

func (engine *Engine) finite(ctx context.Context, run *workspaceRun, repository RepositoryConfig, target Target, command Command, ports map[string]int, environment map[string]string) error {
	engine.mu.Lock()
	runState := copyJSON(run.result)
	engine.mu.Unlock()
	spec, err := commandSpec(command, repository, target, runState, ports, environment)
	if err != nil {
		return err
	}
	run.logs.remember(spec.Script, spec.Environment)
	process, err := launchProcess(ctx, spec, run.logs)
	if err != nil {
		return err
	}
	defer process.stop()
	seconds := command.TimeoutSeconds
	if seconds <= 0 {
		seconds = 15 * 60
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("The command timed out. Open the logs, fix the setup, then run again.")
	case <-process.done:
		if process.err != nil {
			return errors.New("The command exited with an error. Open the logs, fix the setup, then run again.")
		}
		return nil
	}
}

func commandSpec(command Command, repository RepositoryConfig, target Target, run Run, ports map[string]int, portEnvironment map[string]string) (processSpec, error) {
	directory := command.Directory
	if directory == "" {
		directory = "."
	}
	joined := filepath.Join(run.Path, directory)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return processSpec{}, errors.New("the command's working directory is missing; check workspace setup")
	}
	relative, err := filepath.Rel(run.Path, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return processSpec{}, errors.New("the command's working directory leaves this workspace")
	}
	values := map[string]string{}
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "SHELL"} {
		if value, found := os.LookupEnv(name); found {
			values[name] = value
		}
	}
	if values["PATH"] == "" {
		values["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	for _, layer := range []map[string]string{target.Environment, command.Environment} {
		for name, value := range layer {
			value = strings.ReplaceAll(value, "{workspace}", run.Path)
			value = strings.ReplaceAll(value, "{repository}", repository.Path)
			value = strings.ReplaceAll(value, "{instance}", run.ID)
			for key, port := range ports {
				value = strings.ReplaceAll(value, "{port:"+key+"}", strconv.Itoa(port))
			}
			if strings.Contains(value, "{port:") {
				return processSpec{}, errors.New("the command refers to a port from a service that is not selected")
			}
			values[name] = value
		}
	}
	for name, value := range portEnvironment {
		values[name] = value
	}
	assignedPorts, _ := json.Marshal(ports)
	values["SWITCHYARD_PORTS"] = string(assignedPorts)
	values["SWITCHYARD_WORKSPACE"] = run.Path
	values["SWITCHYARD_REPOSITORY"] = repository.Path
	environment := make([]string, 0, len(values))
	for name, value := range values {
		environment = append(environment, name+"="+value)
	}
	sort.Strings(environment)
	return processSpec{Script: command.Script, Directory: resolved, Environment: environment}, nil
}

func waitReady(ctx context.Context, process *managedProcess, readiness Readiness, port int, checkServices func() error) error {
	seconds := readiness.TimeoutSeconds
	if seconds <= 0 {
		seconds = 60
	}
	deadline := time.NewTimer(time.Duration(seconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-process.done:
			return errors.New("service exited before it was ready. Open the logs for the startup error.")
		case <-deadline.C:
			return errors.New("service did not become ready in time. Open the logs for missing setup or startup errors.")
		case <-ticker.C:
			if err := checkServices(); err != nil {
				return err
			}
			ready, err := probeReady(ctx, readiness, port)
			if err != nil {
				return err
			}
			if ready {
				return nil
			}
		}
	}
}

func probeReady(ctx context.Context, readiness Readiness, port int) (bool, error) {
	if readiness.Port == "" {
		return true, nil
	}
	address := "127.0.0.1:" + strconv.Itoa(port)
	if readiness.Path == "" {
		dialer := net.Dialer{Timeout: 250 * time.Millisecond}
		connection, err := dialer.DialContext(ctx, "tcp4", address)
		if err != nil {
			return false, nil
		}
		_ = connection.Close()
		return true, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+readiness.Path, nil)
	if err != nil {
		return false, errors.New("the configured readiness URL is invalid")
	}
	client := http.Client{Timeout: 500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return false, nil
	}
	_ = response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300, nil
}

func selectedServices(repository RepositoryConfig, selected []string) ([]ServiceConfig, error) {
	byID := make(map[string]ServiceConfig)
	for _, service := range repository.Services {
		byID[service.ID] = service
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	result := []ServiceConfig{}
	var add func(string) error
	add = func(id string) error {
		if visited[id] {
			return nil
		}
		if visiting[id] {
			return errors.New("service dependencies contain a cycle")
		}
		service, found := byID[id]
		if !found {
			return fmt.Errorf("service %q is not configured", id)
		}
		visiting[id] = true
		for _, dependency := range service.Dependencies {
			if err := add(dependency); err != nil {
				return err
			}
		}
		visited[id] = true
		visiting[id] = false
		result = append(result, service)
		return nil
	}
	for _, id := range selected {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}
