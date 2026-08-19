package portlease

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
)

func TestReserveProbesOperatingSystem(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	allocator := newTestAllocator(t, Config{FirstPort: port, LastPort: port})
	key := Key{EnvironmentID: "env_01", ServiceID: "organizer", Purpose: "http"}
	_, err = allocator.Reserve(context.Background(), key)
	if !errors.Is(err, ErrNoAvailablePorts) {
		t.Fatalf("occupied port error: got %v, want %v", err, ErrNoAvailablePorts)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	lease, err := allocator.Reserve(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Port != port {
		t.Fatalf("leased port: got %d, want %d", lease.Port, port)
	}

	listener, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := allocator.CheckBeforeLaunch(context.Background(), key); err == nil {
		t.Fatal("launch check accepted a foreign listener")
	} else {
		var unavailable *PortUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("launch check error: got %T, want *PortUnavailableError", err)
		}
	}
}

func TestReserveRejectsWildcardListenerOnDarwin(t *testing.T) {
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	if port == 65535 {
		t.Skip("cannot construct a bounded fallback after the listener port")
	}

	allocator, err := NewAllocator(Config{
		Host: "127.0.0.1", FirstPort: port + 1, LastPort: port + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := Key{EnvironmentID: "environment-wildcard", ServiceID: "service", Purpose: "http"}
	lease, err := allocator.Reserve(context.Background(), key, port)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Port == port {
		t.Fatalf("reserved wildcard-owned listener port %d", port)
	}
}

func TestReserveIsStableForAServicePurpose(t *testing.T) {
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30010,
		Probe:     func(context.Context, string, int) error { return nil },
	})
	key := Key{EnvironmentID: "env_01", ServiceID: "organizer", Purpose: "http"}
	first, err := allocator.Reserve(context.Background(), key, 7005)
	if err != nil {
		t.Fatal(err)
	}
	second, err := allocator.Reserve(context.Background(), key, 7006)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("stable lease changed: first %+v, second %+v", first, second)
	}
}

func TestConcurrentReservationsAreDistinct(t *testing.T) {
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30199,
		Probe:     func(context.Context, string, int) error { return nil },
	})

	const reservationCount = 100
	ports := make(chan int, reservationCount)
	errorsFound := make(chan error, reservationCount)
	var waitGroup sync.WaitGroup
	for index := 0; index < reservationCount; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			lease, err := allocator.Reserve(context.Background(), Key{
				EnvironmentID: fmt.Sprintf("env_%03d", index),
				ServiceID:     "organizer",
				Purpose:       "http",
			})
			if err != nil {
				errorsFound <- err
				return
			}
			ports <- lease.Port
		}(index)
	}
	waitGroup.Wait()
	close(ports)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}

	uniquePorts := make(map[int]struct{}, reservationCount)
	for port := range ports {
		if _, duplicate := uniquePorts[port]; duplicate {
			t.Fatalf("port %d was leased twice", port)
		}
		uniquePorts[port] = struct{}{}
	}
	if got := len(uniquePorts); got != reservationCount {
		t.Fatalf("lease count: got %d, want %d", got, reservationCount)
	}
}

func TestReserveSetRollsBackEveryNewLeaseOnFailure(t *testing.T) {
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30000,
		Probe:     func(context.Context, string, int) error { return nil },
	})
	_, err := allocator.ReserveSet(context.Background(), []Reservation{
		{Key: Key{EnvironmentID: "env_01", ServiceID: "nonprofit-service", Purpose: "http"}},
		{Key: Key{EnvironmentID: "env_01", ServiceID: "nonprofit-service", Purpose: "lambda"}},
	})
	if !errors.Is(err, ErrNoAvailablePorts) {
		t.Fatalf("set error: got %v, want %v", err, ErrNoAvailablePorts)
	}
	if leases := allocator.Leases(); len(leases) != 0 {
		t.Fatalf("failed set left partial leases: %+v", leases)
	}
}

func TestReserveSetCancellationRollsBackPartialLeases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	probes := 0
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30001,
		Probe: func(context.Context, string, int) error {
			probes++
			if probes == 1 {
				cancel()
			}
			return nil
		},
	})
	_, err := allocator.ReserveSet(ctx, []Reservation{
		{Key: Key{EnvironmentID: "env_01", ServiceID: "nonprofit-service", Purpose: "http"}},
		{Key: Key{EnvironmentID: "env_01", ServiceID: "nonprofit-service", Purpose: "lambda"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("set error: got %v, want %v", err, context.Canceled)
	}
	if leases := allocator.Leases(); len(leases) != 0 {
		t.Fatalf("cancelled set left partial leases: %+v", leases)
	}
}

func TestReserveSetPreservesEarlierStableLeaseOnFailure(t *testing.T) {
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30000,
		Probe:     func(context.Context, string, int) error { return nil },
	})
	stableKey := Key{EnvironmentID: "env_01", ServiceID: "organizer", Purpose: "http"}
	stableLease, err := allocator.Reserve(context.Background(), stableKey)
	if err != nil {
		t.Fatal(err)
	}
	_, err = allocator.ReserveSet(context.Background(), []Reservation{
		{Key: stableKey},
		{Key: Key{EnvironmentID: "env_01", ServiceID: "nonprofit-service", Purpose: "http"}},
	})
	if !errors.Is(err, ErrNoAvailablePorts) {
		t.Fatalf("set error: got %v, want %v", err, ErrNoAvailablePorts)
	}
	leases := allocator.Leases()
	if len(leases) != 1 || leases[0] != stableLease {
		t.Fatalf("stable lease changed after rollback: got %+v, want %+v", leases, stableLease)
	}
}

func TestRestoreRehydratesExactLeaseWithoutProbingBoundPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	probes := 0
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30010,
		Probe: func(context.Context, string, int) error {
			probes++
			return errors.New("probe must not run during restore")
		},
	})
	lease := Lease{
		Key:  Key{EnvironmentID: "env_01", ServiceID: "organizer", Purpose: "http"},
		Host: "127.0.0.1", Port: port,
	}
	if err := allocator.Restore([]Lease{lease}); err != nil {
		t.Fatal(err)
	}
	if probes != 0 {
		t.Fatalf("restore probed the operating system %d times", probes)
	}
	if got := allocator.Leases(); len(got) != 1 || got[0] != lease {
		t.Fatalf("restored leases: got %+v, want %+v", got, lease)
	}
	if err := allocator.Restore([]Lease{lease}); err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
}

func TestRestoreRejectsConflictsAtomically(t *testing.T) {
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30010,
		Probe:     func(context.Context, string, int) error { return nil },
	})
	stable := Lease{
		Key:  Key{EnvironmentID: "env_stable", ServiceID: "organizer", Purpose: "http"},
		Host: "127.0.0.1", Port: 30000,
	}
	if err := allocator.Restore([]Lease{stable}); err != nil {
		t.Fatal(err)
	}
	conflicting := []Lease{
		{
			Key:  Key{EnvironmentID: "env_new", ServiceID: "organizer", Purpose: "http"},
			Host: "127.0.0.1", Port: 30001,
		},
		{
			Key:  Key{EnvironmentID: "env_other", ServiceID: "organizer", Purpose: "http"},
			Host: "127.0.0.1", Port: stable.Port,
		},
	}
	if err := allocator.Restore(conflicting); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("restore conflict: got %v, want %v", err, ErrLeaseConflict)
	}
	if got := allocator.Leases(); len(got) != 1 || got[0] != stable {
		t.Fatalf("failed restore changed leases: got %+v, want %+v", got, stable)
	}
}

func TestRestoreRejectsMalformedLease(t *testing.T) {
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30010,
		Probe:     func(context.Context, string, int) error { return nil },
	})
	tests := []Lease{
		{Key: Key{EnvironmentID: "env", ServiceID: "service", Purpose: "http"}, Host: "localhost", Port: 30000},
		{Key: Key{EnvironmentID: "env", ServiceID: "service", Purpose: "http"}, Host: "127.0.0.1", Port: 0},
		{Key: Key{EnvironmentID: "", ServiceID: "service", Purpose: "http"}, Host: "127.0.0.1", Port: 30000},
	}
	for _, lease := range tests {
		if err := allocator.Restore([]Lease{lease}); err == nil {
			t.Fatalf("restore accepted malformed lease: %+v", lease)
		}
	}
	if leases := allocator.Leases(); len(leases) != 0 {
		t.Fatalf("malformed restores changed leases: %+v", leases)
	}
}

func TestReleaseMakesPortAvailable(t *testing.T) {
	allocator := newTestAllocator(t, Config{
		FirstPort: 30000,
		LastPort:  30000,
		Probe:     func(context.Context, string, int) error { return nil },
	})
	firstKey := Key{EnvironmentID: "env_01", ServiceID: "organizer", Purpose: "http"}
	if _, err := allocator.Reserve(context.Background(), firstKey); err != nil {
		t.Fatal(err)
	}
	if !allocator.Release(firstKey) {
		t.Fatal("existing lease was not released")
	}
	secondKey := Key{EnvironmentID: "env_02", ServiceID: "organizer", Purpose: "http"}
	if _, err := allocator.Reserve(context.Background(), secondKey); err != nil {
		t.Fatal(err)
	}
}

func TestAllocatorRejectsNonLoopbackHost(t *testing.T) {
	_, err := NewAllocator(Config{Host: "0.0.0.0", FirstPort: 30000, LastPort: 30010})
	if err == nil {
		t.Fatal("allocator accepted a non-loopback host")
	}
}

func newTestAllocator(t *testing.T, config Config) *Allocator {
	t.Helper()
	allocator, err := NewAllocator(config)
	if err != nil {
		t.Fatal(err)
	}
	return allocator
}
