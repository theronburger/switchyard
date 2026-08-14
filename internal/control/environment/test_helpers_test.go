package environment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

type memoryJournal struct {
	mutex      sync.Mutex
	operations map[string]OperationRecord
	order      []string
	current    map[string]EnvironmentResult
	events     []string
}

func newMemoryJournal() *memoryJournal {
	return &memoryJournal{
		operations: make(map[string]OperationRecord),
		current:    make(map[string]EnvironmentResult),
	}
}

func (journal *memoryJournal) Create(_ context.Context, operation OperationRecord) error {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	if _, exists := journal.operations[operation.ID]; exists {
		return errors.New("duplicate operation")
	}
	journal.operations[operation.ID] = cloneOperation(operation)
	journal.order = append(journal.order, operation.ID)
	journal.events = append(journal.events, "create:"+operation.ID)
	return nil
}

func (journal *memoryJournal) Update(_ context.Context, operation OperationRecord) error {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	if _, exists := journal.operations[operation.ID]; !exists {
		return errors.New("operation does not exist")
	}
	journal.operations[operation.ID] = cloneOperation(operation)
	journal.events = append(journal.events, "update:"+operation.ID+":"+string(operation.Phase))
	return nil
}

func (journal *memoryJournal) Publish(
	_ context.Context,
	operation OperationRecord,
	result EnvironmentResult,
) error {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	if _, exists := journal.operations[operation.ID]; !exists {
		return errors.New("operation does not exist")
	}
	journal.operations[operation.ID] = cloneOperation(operation)
	journal.current[result.EnvironmentID] = cloneEnvironment(result)
	journal.events = append(journal.events, "publish:"+operation.ID)
	return nil
}

func (journal *memoryJournal) Current(_ context.Context, environmentID string) (EnvironmentResult, bool, error) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	result, exists := journal.current[environmentID]
	return cloneEnvironment(result), exists, nil
}

func (journal *memoryJournal) Incomplete(context.Context) ([]OperationRecord, error) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	operations := make([]OperationRecord, 0)
	for _, operationID := range journal.order {
		operation := journal.operations[operationID]
		if operation.State == domain.OperationPending || operation.State == domain.OperationRunning {
			operations = append(operations, cloneOperation(operation))
		}
	}
	return operations, nil
}

func (journal *memoryJournal) putOperation(operation OperationRecord) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	journal.operations[operation.ID] = cloneOperation(operation)
	journal.order = append(journal.order, operation.ID)
}

func (journal *memoryJournal) putCurrent(result EnvironmentResult) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	journal.current[result.EnvironmentID] = cloneEnvironment(result)
}

func (journal *memoryJournal) operation(operationID string) OperationRecord {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	return cloneOperation(journal.operations[operationID])
}

func (journal *memoryJournal) persistedRollback(operationID string, kind RollbackKind) bool {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	operation, exists := journal.operations[operationID]
	if !exists {
		return false
	}
	for _, entry := range operation.Rollback {
		if entry.Kind == kind && entry.Armed {
			return true
		}
	}
	return false
}

func cloneOperation(operation OperationRecord) OperationRecord {
	copy := operation
	copy.Intent = cloneIntent(operation.Intent)
	copy.Rollback = append([]RollbackEntry(nil), operation.Rollback...)
	for index := range copy.Rollback {
		copy.Rollback[index].PortKeys = append([]portlease.Key(nil), operation.Rollback[index].PortKeys...)
		copy.Rollback[index].Leases = cloneLeases(operation.Rollback[index].Leases)
		copy.Rollback[index].Projection = cloneProjection(operation.Rollback[index].Projection)
		copy.Rollback[index].Infrastructure = cloneGoals(operation.Rollback[index].Infrastructure)
		copy.Rollback[index].Process = cloneService(operation.Rollback[index].Process)
	}
	if operation.Target != nil {
		copy.Target = environmentPointer(*operation.Target)
	}
	return copy
}

type fakePorts struct {
	mutex    sync.Mutex
	next     int
	byKey    map[portlease.Key]portlease.Lease
	calls    *[]string
	guard    func(RollbackKind) bool
	guardErr error
}

func newFakePorts(first int, calls *[]string) *fakePorts {
	return &fakePorts{next: first, byKey: make(map[portlease.Key]portlease.Lease), calls: calls}
}

func (ports *fakePorts) ReserveSet(
	ctx context.Context,
	reservations []portlease.Reservation,
) ([]portlease.Lease, error) {
	ports.mutex.Lock()
	defer ports.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ports.guard != nil && !ports.guard(RollbackPorts) {
		return nil, ports.guardErr
	}
	leases := make([]portlease.Lease, 0, len(reservations))
	for _, reservation := range reservations {
		if existing, exists := ports.byKey[reservation.Key]; exists {
			leases = append(leases, existing)
			continue
		}
		lease := portlease.Lease{Key: reservation.Key, Host: "127.0.0.1", Port: ports.next}
		ports.next++
		ports.byKey[lease.Key] = lease
		leases = append(leases, lease)
	}
	if ports.calls != nil {
		*ports.calls = append(*ports.calls, "reserve-ports")
	}
	return leases, nil
}

func (ports *fakePorts) CheckBeforeLaunch(ctx context.Context, key portlease.Key) error {
	ports.mutex.Lock()
	defer ports.mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := ports.byKey[key]; !exists {
		return portlease.ErrLeaseNotFound
	}
	return nil
}

func (ports *fakePorts) Release(key portlease.Key) bool {
	ports.mutex.Lock()
	defer ports.mutex.Unlock()
	if _, exists := ports.byKey[key]; !exists {
		return false
	}
	delete(ports.byKey, key)
	if ports.calls != nil {
		*ports.calls = append(*ports.calls, "release-port")
	}
	return true
}

func (ports *fakePorts) Leases() []portlease.Lease {
	ports.mutex.Lock()
	defer ports.mutex.Unlock()
	leases := make([]portlease.Lease, 0, len(ports.byKey))
	for _, lease := range ports.byKey {
		leases = append(leases, lease)
	}
	return leases
}

func (ports *fakePorts) leaseCount() int {
	ports.mutex.Lock()
	defer ports.mutex.Unlock()
	return len(ports.byKey)
}

type fakeProjection struct {
	mutex        sync.Mutex
	journal      *memoryJournal
	operationID  string
	calls        *[]string
	applyErr     error
	blockApply   <-chan struct{}
	enteredApply chan<- struct{}
	active       int
	maximum      int
}

func (projection *fakeProjection) Plan(
	_ context.Context,
	environmentID string,
	runID string,
	request ProjectionRequest,
	_ []portlease.Lease,
) (ProjectionChange, error) {
	return ProjectionChange{
		ID: request.ID, EnvironmentID: environmentID, RunID: runID,
		RollbackToken: "rollback-" + environmentID, Owned: true,
	}, nil
}

func (projection *fakeProjection) Apply(ctx context.Context, _ ProjectionChange) error {
	if projection.journal != nil && !projection.journal.persistedRollback(projection.operationID, RollbackProjection) {
		return errors.New("projection applied before rollback was persisted")
	}
	projection.mutex.Lock()
	projection.active++
	if projection.active > projection.maximum {
		projection.maximum = projection.active
	}
	projection.mutex.Unlock()
	defer func() {
		projection.mutex.Lock()
		projection.active--
		projection.mutex.Unlock()
	}()
	if projection.enteredApply != nil {
		projection.enteredApply <- struct{}{}
	}
	if projection.blockApply != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-projection.blockApply:
		}
	}
	if projection.calls != nil {
		*projection.calls = append(*projection.calls, "apply-projection")
	}
	return projection.applyErr
}

func (projection *fakeProjection) Rollback(context.Context, ProjectionChange) error {
	if projection.calls != nil {
		*projection.calls = append(*projection.calls, "rollback-projection")
	}
	return nil
}

func (projection *fakeProjection) maxConcurrent() int {
	projection.mutex.Lock()
	defer projection.mutex.Unlock()
	return projection.maximum
}

type fakeInfrastructure struct {
	journal     *memoryJournal
	operationID string
	calls       *[]string
	ensureCalls int
	stopCalls   int
}

func (host *fakeInfrastructure) Ensure(context.Context, []containerhost.Goal) error {
	if host.journal != nil && !host.journal.persistedRollback(host.operationID, RollbackInfrastructure) {
		return errors.New("infrastructure ensured before rollback was persisted")
	}
	host.ensureCalls++
	if host.calls != nil {
		*host.calls = append(*host.calls, "ensure-infrastructure")
	}
	return nil
}

func (host *fakeInfrastructure) StopOwned(context.Context, []containerhost.Goal) error {
	host.stopCalls++
	if host.calls != nil {
		*host.calls = append(*host.calls, "stop-infrastructure")
	}
	return nil
}

type fakeProcesses struct {
	journal     *memoryJournal
	operationID string
	calls       *[]string
	starts      int
	stops       int
	stopErr     error
	startSpecs  []processhost.LaunchSpec
}

func (host *fakeProcesses) Start(_ context.Context, spec processhost.LaunchSpec) (processhost.Ownership, error) {
	if host.journal != nil && !host.journal.persistedRollback(host.operationID, RollbackProcess) {
		return processhost.Ownership{}, errors.New("process started before rollback was persisted")
	}
	host.starts++
	specCopy := spec
	specCopy.Arguments = append([]string(nil), spec.Arguments...)
	specCopy.Environment = append([]string(nil), spec.Environment...)
	host.startSpecs = append(host.startSpecs, specCopy)
	if host.calls != nil {
		*host.calls = append(*host.calls, "start-process")
	}
	return processhost.Ownership{
		SchemaVersion: processhost.OwnershipSchemaVersion,
		EnvironmentID: spec.EnvironmentID,
		ServiceID:     spec.ServiceID,
		RunID:         spec.RunID,
		State:         "running",
		StdoutPath:    filepath.Join(spec.RunDirectory, processhost.StdoutLogFileName),
		StderrPath:    filepath.Join(spec.RunDirectory, processhost.StderrLogFileName),
	}, nil
}

func (host *fakeProcesses) Stop(context.Context, string) (processhost.Observation, error) {
	host.stops++
	if host.calls != nil {
		*host.calls = append(*host.calls, "stop-process")
	}
	return processhost.Observation{State: "stopped"}, host.stopErr
}

func (*fakeProcesses) Reconcile(context.Context, string) (processhost.Observation, error) {
	return processhost.Observation{State: "running"}, nil
}

type fakeReadiness struct {
	err     error
	entered chan<- struct{}
	block   <-chan struct{}
}

type staticPlanner struct {
	mutex sync.Mutex
	plan  ExecutionPlan
	seen  []PlanningRequest
}

type portBindingPlanner struct {
	runDirectory string
	journal      *memoryJournal
	operationID  string
	seen         []PlanningRequest
}

func (planner *portBindingPlanner) Build(request PlanningRequest) (ExecutionPlan, error) {
	planner.seen = append(planner.seen, request)
	if len(request.AssignedPorts) != 1 {
		return ExecutionPlan{}, errors.New("expected one assigned port")
	}
	if planner.journal != nil {
		operation := planner.journal.operation(planner.operationID)
		persisted := false
		for _, entry := range operation.Rollback {
			if entry.Kind == RollbackPorts && entry.Applied && len(entry.Leases) == 1 &&
				entry.Leases[0] == request.AssignedPorts[0] {
				persisted = true
			}
		}
		if !persisted {
			return ExecutionPlan{}, errors.New("assigned port was not persisted before planning")
		}
	}
	serviceID := request.AssignedPorts[0].Key.ServiceID
	return ExecutionPlan{Services: []ServiceLaunch{{
		ID: serviceID,
		Process: processhost.LaunchSpec{
			EnvironmentID: request.EnvironmentID,
			ServiceID:     serviceID,
			RunID:         request.RunID,
			Executable:    "/bin/echo",
			Directory:     "/tmp",
			RunDirectory:  planner.runDirectory,
			Environment:   []string{"PORT=" + strconv.Itoa(request.AssignedPorts[0].Port)},
		},
		PortKeys:  []portlease.Key{request.AssignedPorts[0].Key},
		Readiness: ReadinessSpec{ID: "http"},
	}}}, nil
}

func (planner *staticPlanner) Build(request PlanningRequest) (ExecutionPlan, error) {
	planner.mutex.Lock()
	defer planner.mutex.Unlock()
	planner.seen = append(planner.seen, request)
	return cloneExecutionPlan(planner.plan), nil
}

func cloneExecutionPlan(plan ExecutionPlan) ExecutionPlan {
	copy := plan
	if plan.Projection != nil {
		projection := *plan.Projection
		copy.Projection = &projection
	}
	copy.Infrastructure = cloneGoals(plan.Infrastructure)
	copy.Services = append([]ServiceLaunch(nil), plan.Services...)
	for index := range copy.Services {
		copy.Services[index].PortKeys = append([]portlease.Key(nil), plan.Services[index].PortKeys...)
		copy.Services[index].Process.Arguments = append(
			[]string(nil), plan.Services[index].Process.Arguments...,
		)
		copy.Services[index].Process.Environment = append(
			[]string(nil), plan.Services[index].Process.Environment...,
		)
	}
	return copy
}

func (readiness *fakeReadiness) WaitReady(ctx context.Context, _ ReadinessTarget) error {
	if readiness.entered != nil {
		readiness.entered <- struct{}{}
	}
	if readiness.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readiness.block:
		}
	}
	if readiness.err != nil {
		return readiness.err
	}
	return nil
}

func (*fakeReadiness) CheckHealth(context.Context, ReadinessTarget) (HealthReport, error) {
	return HealthReport{Readiness: "ready", Health: "healthy"}, nil
}

func fullStartRequest(t *testing.T, operationID, environmentID, runID string) StartRequest {
	t.Helper()
	serviceID := "service_web"
	return StartRequest{
		OperationID: operationID, EnvironmentID: environmentID, RunID: runID,
		Ports: []portlease.Reservation{{
			Key:            portlease.Key{EnvironmentID: environmentID, ServiceID: serviceID, Purpose: "http"},
			PreferredPorts: []int{7000},
		}},
		Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{serviceID}},
	}
}

func fullExecutionPlan(t *testing.T, environmentID, runID string) ExecutionPlan {
	t.Helper()
	serviceID := "service_web"
	return ExecutionPlan{
		Projection: &ProjectionRequest{ID: "projection"},
		Infrastructure: []containerhost.Goal{{
			Kind: containerhost.ResourceContainer, Name: "infra-" + environmentID, Image: "elasticmq:latest",
			Identity: containerhost.Identity{
				EnvironmentID: environmentID, ServiceID: "infra_queue", RunID: runID, InstanceID: "instance_queue",
			},
			DesiredState: containerhost.DesiredRunning,
		}},
		Services: []ServiceLaunch{{
			ID: serviceID,
			Process: processhost.LaunchSpec{
				EnvironmentID: environmentID, ServiceID: serviceID, RunID: runID,
				Executable: "/bin/echo", Directory: "/tmp", RunDirectory: filepath.Join(t.TempDir(), environmentID),
			},
			PortKeys: []portlease.Key{{
				EnvironmentID: environmentID, ServiceID: serviceID, Purpose: "http",
			}},
			Readiness: ReadinessSpec{ID: "http"},
		}},
	}
}

func testServiceResult(environmentID, runID string, owned bool) ServiceResult {
	return ServiceResult{
		ID: "service_web", EnvironmentID: environmentID, RunID: runID,
		OwnershipPath: filepath.Join(os.TempDir(), environmentID, processhost.OwnershipFileName), Owned: owned,
		Process: processhost.Ownership{
			EnvironmentID: environmentID, ServiceID: "service_web", RunID: runID,
		},
	}
}
