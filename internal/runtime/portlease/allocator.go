package portlease

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"syscall"
)

var (
	ErrLeaseNotFound    = errors.New("port lease not found")
	ErrLeaseConflict    = errors.New("port lease conflicts with restored ownership")
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

type Reservation struct {
	Key            Key
	PreferredPorts []int
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

type NoAvailablePortError struct {
	Key Key
}

func (err *NoAvailablePortError) Error() string {
	return fmt.Sprintf("%s: environment %q service %q purpose %q", ErrNoAvailablePorts, err.Key.EnvironmentID, err.Key.ServiceID, err.Key.Purpose)
}

func (err *NoAvailablePortError) Unwrap() error {
	return ErrNoAvailablePorts
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
	leases, err := allocator.ReserveSet(ctx, []Reservation{{Key: key, PreferredPorts: preferredPorts}})
	if err != nil {
		return Lease{}, err
	}
	return leases[0], nil
}

// Restore atomically re-seeds exact leases from durable environment state.
// It deliberately does not probe the operating system: a healthy restored
// service is expected to already have its port bound. Callers must restore all
// persisted leases before accepting new reservations.
func (allocator *Allocator) Restore(leases []Lease) error {
	allocator.mutex.Lock()
	defer allocator.mutex.Unlock()

	batchKeys := make(map[Key]struct{}, len(leases))
	batchPorts := make(map[int]struct{}, len(leases))
	for _, lease := range leases {
		if err := validateKey(lease.Key); err != nil {
			return err
		}
		if lease.Host != allocator.host {
			return fmt.Errorf("%w: lease host does not match allocator host", ErrLeaseConflict)
		}
		if lease.Port < 1 || lease.Port > 65535 {
			return fmt.Errorf("%w: lease port is outside 1..65535", ErrLeaseConflict)
		}
		if _, duplicate := batchKeys[lease.Key]; duplicate {
			return fmt.Errorf("%w: duplicate lease key", ErrLeaseConflict)
		}
		if _, duplicate := batchPorts[lease.Port]; duplicate {
			return fmt.Errorf("%w: duplicate lease port", ErrLeaseConflict)
		}
		batchKeys[lease.Key] = struct{}{}
		batchPorts[lease.Port] = struct{}{}

		if existing, exists := allocator.byKey[lease.Key]; exists && existing != lease {
			return fmt.Errorf("%w: lease key already owns another port", ErrLeaseConflict)
		}
		if existingKey, exists := allocator.byPort[lease.Port]; exists && existingKey != lease.Key {
			return fmt.Errorf("%w: lease port already has another owner", ErrLeaseConflict)
		}
	}

	for _, lease := range leases {
		allocator.byKey[lease.Key] = lease
		allocator.byPort[lease.Port] = lease.Key
	}
	return nil
}

func (allocator *Allocator) ReserveSet(ctx context.Context, reservations []Reservation) ([]Lease, error) {
	if len(reservations) == 0 {
		return nil, errors.New("at least one port reservation is required")
	}
	seenKeys := make(map[Key]struct{}, len(reservations))
	for _, reservation := range reservations {
		if err := validateKey(reservation.Key); err != nil {
			return nil, err
		}
		if _, duplicate := seenKeys[reservation.Key]; duplicate {
			return nil, fmt.Errorf("duplicate port reservation for %+v", reservation.Key)
		}
		seenKeys[reservation.Key] = struct{}{}
	}

	allocator.mutex.Lock()
	defer allocator.mutex.Unlock()

	leases := make([]Lease, 0, len(reservations))
	newLeases := make([]Lease, 0, len(reservations))
	rollback := func() {
		for _, lease := range newLeases {
			delete(allocator.byKey, lease.Key)
			delete(allocator.byPort, lease.Port)
		}
	}

	for _, reservation := range reservations {
		if err := ctx.Err(); err != nil {
			rollback()
			return nil, err
		}
		if existing, exists := allocator.byKey[reservation.Key]; exists {
			leases = append(leases, existing)
			continue
		}

		lease, err := allocator.reserveLocked(ctx, reservation)
		if err != nil {
			rollback()
			return nil, err
		}
		newLeases = append(newLeases, lease)
		leases = append(leases, lease)
	}
	return leases, nil
}

func (allocator *Allocator) reserveLocked(ctx context.Context, reservation Reservation) (Lease, error) {
	for _, port := range allocator.candidates(reservation.PreferredPorts) {
		if err := ctx.Err(); err != nil {
			return Lease{}, err
		}
		if _, alreadyLeased := allocator.byPort[port]; alreadyLeased {
			continue
		}
		if err := allocator.probe(ctx, allocator.host, port); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return Lease{}, contextErr
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return Lease{}, err
		}

		lease := Lease{Key: reservation.Key, Host: allocator.host, Port: port}
		allocator.byKey[reservation.Key] = lease
		allocator.byPort[port] = reservation.Key
		return lease, nil
	}
	return Lease{}, &NoAvailablePortError{Key: reservation.Key}
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
	address := net.JoinHostPort(host, strconv.Itoa(port))
	connection, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	if dialErr == nil {
		_ = connection.Close()
		return errors.New("port already accepts TCP connections")
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return dialErr
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return listener.Close()
}
