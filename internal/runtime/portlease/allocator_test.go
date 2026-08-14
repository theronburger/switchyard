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
