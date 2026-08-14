package health

import "time"

const LoopbackHost = "127.0.0.1"

type ProbeKind string

const (
	ProbeKindTCP  ProbeKind = "tcp"
	ProbeKindHTTP ProbeKind = "http"
)

type ResultCode string

const (
	ResultOK               ResultCode = "ok"
	ResultInvalid          ResultCode = "invalid"
	ResultUnavailable      ResultCode = "unavailable"
	ResultTimeout          ResultCode = "timeout"
	ResultUnexpectedStatus ResultCode = "unexpected_status"
)

type State string

const (
	StateStarting  State = "starting"
	StateHealthy   State = "healthy"
	StateDegraded  State = "degraded"
	StateUnhealthy State = "unhealthy"
)

type StatusRange struct {
	Minimum int `json:"minimum"`
	Maximum int `json:"maximum"`
}

type Lease struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type ProbeSpec struct {
	ID               string        `json:"id"`
	Kind             ProbeKind     `json:"kind"`
	Lease            Lease         `json:"lease"`
	Method           string        `json:"method,omitempty"`
	Path             string        `json:"path,omitempty"`
	AcceptedStatuses []StatusRange `json:"acceptedStatuses,omitempty"`
	Timeout          time.Duration `json:"timeout"`
}

type ProbeResult struct {
	ProbeID    string        `json:"probeId"`
	Kind       ProbeKind     `json:"kind"`
	Code       ResultCode    `json:"code"`
	Success    bool          `json:"success"`
	Status     int           `json:"status,omitempty"`
	Latency    time.Duration `json:"latency"`
	ObservedAt time.Time     `json:"observedAt"`
}

type ServiceSpec struct {
	EnvironmentID    string      `json:"environmentId"`
	ServiceID        string      `json:"serviceId"`
	RunID            string      `json:"runId"`
	Readiness        []ProbeSpec `json:"readiness,omitempty"`
	Health           []ProbeSpec `json:"health,omitempty"`
	FailureThreshold int         `json:"failureThreshold,omitempty"`
}

type ServiceRuntimeState struct {
	EverReady           bool `json:"everReady"`
	ConsecutiveFailures int  `json:"consecutiveFailures"`
}

type Observation struct {
	EnvironmentID  string              `json:"environmentId"`
	ServiceID      string              `json:"serviceId"`
	RunID          string              `json:"runId"`
	State          State               `json:"state"`
	ProcessRunning bool                `json:"processRunning"`
	ReadinessReady bool                `json:"readinessReady"`
	HealthHealthy  bool                `json:"healthHealthy"`
	Readiness      []ProbeResult       `json:"readiness,omitempty"`
	Health         []ProbeResult       `json:"health,omitempty"`
	Runtime        ServiceRuntimeState `json:"runtime"`
	RetryAfter     time.Duration       `json:"retryAfter,omitempty"`
	ObservedAt     time.Time           `json:"observedAt"`
}
