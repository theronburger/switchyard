package workspaces

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"time"
)

type Engine struct {
	mu              sync.Mutex
	configPath      string
	config          Config
	instance        string
	revision        uint64
	closed          bool
	runs            map[string]*workspaceRun
	requests        map[string]*workspaceRun
	requestOrder    []string
	locks           sync.Map
	ports           portPool
	discoveryMu     sync.Mutex
	discovered      []Workspace
	discoveredAt    time.Time
	discoveryConfig string
	discoveryErrors map[string]string
}

func New(configPath string) (*Engine, error) {
	config, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err := SaveConfig(configPath, config); err != nil {
			return nil, err
		}
	}
	return &Engine{configPath: configPath, config: config, instance: newID(), revision: 1,
		runs: make(map[string]*workspaceRun), requests: make(map[string]*workspaceRun)}, nil
}

func (engine *Engine) Config() Config {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return copyJSON(engine.config)
}

func (engine *Engine) InstanceID() string { return engine.instance }

func (engine *Engine) SetConfig(config Config) error {
	config = copyJSON(config)
	if err := ValidateConfig(config); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if err := engine.checkConfigChange(config); err != nil {
		return err
	}
	if err := SaveConfig(engine.configPath, config); err != nil {
		return err
	}
	engine.config = config
	engine.revision++
	return nil
}

func (engine *Engine) ReloadConfig() error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if _, err := os.Stat(engine.configPath); err != nil {
		return errors.New("Cannot read config.json. Restore the file to apply configuration changes.")
	}
	config, err := LoadConfig(engine.configPath)
	if err != nil {
		return fmt.Errorf("Fix config.json to apply changes: %w", err)
	}
	if reflect.DeepEqual(config, engine.config) {
		return nil
	}
	if err := engine.checkConfigChange(config); err != nil {
		return err
	}
	engine.config = config
	engine.revision++
	return nil
}

func (engine *Engine) checkConfigChange(config Config) error {
	if engine.closed {
		return errors.New("Switchyard is shutting down")
	}
	for _, run := range engine.runs {
		if !activeState(run.result.State) {
			continue
		}
		retained := false
		for _, repository := range config.Repositories {
			if repository.ID != run.repositoryID {
				continue
			}
			for _, previous := range engine.config.Repositories {
				if previous.ID == repository.ID && previous.Path == repository.Path {
					retained = true
				}
			}
		}
		if !retained {
			return errors.New("stop workspace runs before removing their repository or changing its directory")
		}
	}
	return nil
}

func (engine *Engine) SetChoices(path string, choices Choices) error {
	choices = copyJSON(choices)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repository, path, err := engine.repository(ctx, path, false)
	if err != nil {
		return err
	}
	unlock := engine.lock(path)
	defer unlock()
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.closed {
		return errors.New("Switchyard is shutting down")
	}
	configured := false
	for _, current := range engine.config.Repositories {
		if current.ID == repository.ID && current.Path == repository.Path {
			repository, configured = current, true
			break
		}
	}
	if !configured {
		return errors.New("the repository configuration changed; select the workspace again")
	}
	if choices.Target == "" {
		choices.Target = repository.DefaultTarget
	}
	targetExists := false
	for _, target := range repository.Targets {
		if target.ID == choices.Target {
			targetExists = true
			break
		}
	}
	if !targetExists {
		return errors.New("the selected target is not configured for this workspace")
	}
	if _, err := selectedServices(repository, choices.Services); err != nil {
		return err
	}
	config := copyJSON(engine.config)
	if config.Choices == nil {
		config.Choices = make(map[string]Choices)
	}
	config.Choices[path] = copyJSON(choices)
	if err := SaveConfig(engine.configPath, config); err != nil {
		return err
	}
	engine.config = config
	engine.revision++
	return nil
}

func (engine *Engine) Status(ctx context.Context) (Snapshot, error) {
	config := engine.Config()
	payload, _ := json.Marshal(config.Repositories)
	engine.discoveryMu.Lock()
	if engine.discoveryConfig != string(payload) || time.Since(engine.discoveredAt) >= 10*time.Second {
		workspaces, err := Discover(ctx, config.Repositories)
		if ctx.Err() != nil {
			engine.discoveryMu.Unlock()
			return Snapshot{}, ctx.Err()
		}
		engine.discoveryErrors = make(map[string]string)
		collectGitErrors(err, engine.discoveryErrors)
		engine.discovered = workspaces
		engine.discoveredAt = time.Now()
		engine.discoveryConfig = string(payload)
	}
	workspaces := copyJSON(engine.discovered)
	discoveryErrors := copyJSON(engine.discoveryErrors)
	engine.discoveryMu.Unlock()
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.revision++
	snapshot := Snapshot{Version: APIVersion, InstanceID: engine.instance, Revision: engine.revision,
		Repositories: []RepositorySummary{}, Workspaces: workspaces}
	if snapshot.Workspaces == nil {
		snapshot.Workspaces = []Workspace{}
	}
	for _, repository := range config.Repositories {
		summary := RepositorySummary{ID: repository.ID, Name: repository.Name, Path: repository.Path,
			DefaultTarget: repository.DefaultTarget, Targets: []TargetSummary{}, Services: []ServiceSummary{}, Error: discoveryErrors[repository.ID]}
		for _, target := range repository.Targets {
			summary.Targets = append(summary.Targets, TargetSummary{ID: target.ID, Name: target.Name, Confirm: target.Confirm})
		}
		for _, service := range repository.Services {
			summary.Services = append(summary.Services, ServiceSummary{ID: service.ID, Name: service.Name, Kind: service.Kind, Dependencies: service.Dependencies})
		}
		snapshot.Repositories = append(snapshot.Repositories, summary)
	}
	for index := range snapshot.Workspaces {
		workspace := &snapshot.Workspaces[index]
		workspace.Choices = copyJSON(engine.config.Choices[workspace.Path])
		if run := engine.runs[workspace.Path]; run != nil {
			result := copyJSON(run.result)
			workspace.Run = &result
		}
	}
	return snapshot, nil
}

func (engine *Engine) repository(ctx context.Context, path string, allowMissing bool) (RepositoryConfig, string, error) {
	physical, err := physicalWorkspacePath(path)
	if err != nil {
		return RepositoryConfig{}, "", err
	}
	config := engine.Config()
	workspaces, err := Discover(ctx, config.Repositories)
	for _, workspace := range workspaces {
		if workspace.Path != physical {
			continue
		}
		if !allowMissing && (workspace.Missing || workspace.GitError != "") {
			return RepositoryConfig{}, "", errors.New("this workspace is unavailable; check its Git directory")
		}
		for _, repository := range config.Repositories {
			if repository.ID == workspace.RepositoryID {
				return repository, physical, nil
			}
		}
	}
	if err != nil {
		return RepositoryConfig{}, "", err
	}
	return RepositoryConfig{}, "", errors.New("this directory is not a worktree of a configured repository")
}

func collectGitErrors(err error, messages map[string]string) {
	if err == nil {
		return
	}
	if failure, ok := err.(*GitError); ok {
		messages[failure.RepositoryID] = failure.Reason
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			collectGitErrors(cause, messages)
		}
	}
}

func (engine *Engine) lock(path string) func() {
	value, _ := engine.locks.LoadOrStore(path, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (engine *Engine) PrunePlan(ctx context.Context, path string) (PrunePlan, error) {
	repository, path, err := engine.repository(ctx, path, true)
	if err != nil {
		return PrunePlan{}, err
	}
	unlock := engine.lock(path)
	defer unlock()
	plan, err := GitPrunePlan(ctx, repository, path)
	if err != nil {
		return PrunePlan{}, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if run := engine.runs[path]; run != nil && activeState(run.result.State) {
		plan.Blockers = append(plan.Blockers, "Stop this workspace before pruning it.")
	}
	return plan, nil
}

func (engine *Engine) Prune(ctx context.Context, path string) error {
	repository, path, err := engine.repository(ctx, path, true)
	if err != nil {
		return err
	}
	unlock := engine.lock(path)
	defer unlock()
	engine.mu.Lock()
	active := engine.runs[path] != nil && activeState(engine.runs[path].result.State)
	engine.mu.Unlock()
	if active {
		return errors.New("stop this workspace before pruning it")
	}
	if err := GitPrune(ctx, repository, path); err != nil {
		return err
	}
	engine.mu.Lock()
	delete(engine.runs, path)
	engine.revision++
	engine.mu.Unlock()
	engine.invalidateDiscovery()
	return nil
}

func (engine *Engine) Create(ctx context.Context, request CreateRequest) (string, error) {
	for _, repository := range engine.Config().Repositories {
		if repository.ID != request.RepositoryID {
			continue
		}
		unlock := engine.lock(repository.Path)
		defer unlock()
		path, err := GitCreate(ctx, repository, request)
		if err == nil {
			engine.invalidateDiscovery()
		}
		return path, err
	}
	return "", errors.New("repository is not configured")
}

func (engine *Engine) Close(ctx context.Context) error {
	engine.mu.Lock()
	engine.closed = true
	runs := make([]*workspaceRun, 0, len(engine.runs))
	for _, run := range engine.runs {
		run.cancel()
		runs = append(runs, run)
	}
	engine.mu.Unlock()
	for _, run := range runs {
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (engine *Engine) invalidateDiscovery() {
	engine.discoveryMu.Lock()
	engine.discoveredAt = time.Time{}
	engine.discoveryMu.Unlock()
}

func activeState(state string) bool {
	return state == "starting" || state == "running" || state == "stopping"
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(fmt.Errorf("generate run ID: %w", err))
	}
	return hex.EncodeToString(bytes[:])
}

func copyJSON[T any](value T) T {
	payload, _ := json.Marshal(value)
	var copied T
	_ = json.Unmarshal(payload, &copied)
	return copied
}
