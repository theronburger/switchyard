package portlease

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
)

var (
	ErrLeaseNotFound    = errors.New("port lease not found")
	ErrNoAvailablePorts = errors.New("no available ports")
)

type Key struct {
	EnvironmentID string
	ServiceID     string
	Purpose       string
}

type Lease struct {
	Key  Key
	Host string
	Port int
}

type Probe func(ctx context.Context, host string, port int) error

type Config struct {
	Host      string
	FirstPort int
	LastPort  int
	Probe     Probe
}

type Allocator struct {
	mutex     sync.Mutex
	host      string
	firstPort int
	lastPort  int
	probe     Probe
	byKey     map[Key]Lease
	byPort    map[int]Key
}

type PortUnavailableError struct {
	Host string
	Port int
	Err  error
}

func (err *PortUnavailableError) Error() string {
	return fmt.Sprintf("port %s:%d is unavailable: %v", err.Host, err.Port, err.Err)
}

func (err *PortUnavailableError) Unwrap() error {
	return err.Err
}

func NewAllocator(config Config) (*Allocator, error) {
	if config.Host == "" {
		config.Host = "127.0.0.1"
	}
	parsedHost := net.ParseIP(config.Host)
	if parsedHost == nil || !parsedHost.IsLoopback() {
		return nil, fmt.Errorf("port allocator host %q is not loopback", config.Host)
	}
	if config.FirstPort < 1 || config.FirstPort > 65535 {
		return nil, fmt.Errorf("first port %d is outside 1..65535", config.FirstPort)
	}
	if config.LastPort < config.FirstPort || config.LastPort > 65535 {
		return nil, fmt.Errorf("last port %d is outside %d..65535", config.LastPort, config.FirstPort)
	}
	if config.Probe == nil {
		config.Probe = ProbeOperatingSystem
	}
	return &Allocator{
		host:      config.Host,
		firstPort: config.FirstPort,
		lastPort:  config.LastPort,
		probe:     config.Probe,
		byKey:     make(map[Key]Lease),
		byPort:    make(map[int]Key),
	}, nil
}

func (allocator *Allocator) Reserve(ctx context.Context, key Key, preferredPorts ...int) (Lease, error) {
	if err := validateKey(key); err != nil {
		return Lease{}, err
	}

	allocator.mutex.Lock()
	defer allocator.mutex.Unlock()

	if existing, exists := allocator.byKey[key]; exists {
		return existing, nil
	}

	for _, port := range allocator.candidates(preferredPorts) {
		if _, alreadyLeased := allocator.byPort[port]; alreadyLeased {
			continue
		}
		if err := allocator.probe(ctx, allocator.host, port); err != nil {
			continue
		}

		lease := Lease{Key: key, Host: allocator.host, Port: port}
		allocator.byKey[key] = lease
		allocator.byPort[port] = key
		return lease, nil
	}
	return Lease{}, ErrNoAvailablePorts
}

func (allocator *Allocator) CheckBeforeLaunch(ctx context.Context, key Key) error {
	allocator.mutex.Lock()
	defer allocator.mutex.Unlock()

	lease, exists := allocator.byKey[key]
	if !exists {
		return ErrLeaseNotFound
	}
	if err := allocator.probe(ctx, lease.Host, lease.Port); err != nil {
		return &PortUnavailableError{Host: lease.Host, Port: lease.Port, Err: err}
	}
	return nil
}

func (allocator *Allocator) Release(key Key) bool {
	allocator.mutex.Lock()
	defer allocator.mutex.Unlock()

	lease, exists := allocator.byKey[key]
	if !exists {
		return false
	}
	delete(allocator.byKey, key)
	delete(allocator.byPort, lease.Port)
	return true
}

func (allocator *Allocator) Leases() []Lease {
	allocator.mutex.Lock()
	defer allocator.mutex.Unlock()

	leases := make([]Lease, 0, len(allocator.byKey))
	for _, lease := range allocator.byKey {
		leases = append(leases, lease)
	}
	sort.Slice(leases, func(left, right int) bool {
		return leases[left].Port < leases[right].Port
	})
	return leases
}

func (allocator *Allocator) candidates(preferredPorts []int) []int {
	expectedCandidates := len(preferredPorts) + allocator.lastPort - allocator.firstPort + 1
	seen := make(map[int]struct{}, expectedCandidates)
	candidates := make([]int, 0, expectedCandidates)
	appendCandidate := func(port int) {
		if port < 1 || port > 65535 {
			return
		}
		if _, exists := seen[port]; exists {
			return
		}
		seen[port] = struct{}{}
		candidates = append(candidates, port)
	}
	for _, port := range preferredPorts {
		appendCandidate(port)
	}
	for port := allocator.firstPort; port <= allocator.lastPort; port++ {
		appendCandidate(port)
	}
	return candidates
}

func validateKey(key Key) error {
	if key.EnvironmentID == "" {
		return errors.New("environment id is required")
	}
	if key.ServiceID == "" {
		return errors.New("service id is required")
	}
	if key.Purpose == "" {
		return errors.New("port purpose is required")
	}
	return nil
}

func ProbeOperatingSystem(ctx context.Context, host string, port int) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return listener.Close()
}
