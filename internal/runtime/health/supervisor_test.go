package health

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

type sliceEventSource struct {
	events []ServiceEvent
	index  int
}

func (source *sliceEventSource) Next(context.Context) (ServiceEvent, error) {
	if source.index == len(source.events) {
		return ServiceEvent{}, io.EOF
	}
	event := source.events[source.index]
	source.index++
	return event, nil
}

type fixedEventObserver struct {
	observation Observation
}

func (observer fixedEventObserver) ObserveEvent(context.Context, ServiceEvent) (Observation, error) {
	return observer.observation, nil
}

type collectingSink struct {
	observations []Observation
}

func (sink *collectingSink) Record(_ context.Context, observation Observation) error {
	sink.observations = append(sink.observations, observation)
	return nil
}

func TestSupervisorPublishesOnlyRecordedObservations(t *testing.T) {
	t.Parallel()
	source := &sliceEventSource{events: []ServiceEvent{{}, {}}}
	sink := &collectingSink{}
	supervisor := Supervisor{
		Source: source, Observer: fixedEventObserver{observation: Observation{State: StateHealthy}}, Sink: sink,
	}
	if err := supervisor.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.observations) != 2 {
		t.Fatalf("got %d observations, want 2", len(sink.observations))
	}
}

type blockingEventSource struct {
	started sync.Once
	ready   chan struct{}
}

func (source *blockingEventSource) Next(ctx context.Context) (ServiceEvent, error) {
	source.started.Do(func() { close(source.ready) })
	<-ctx.Done()
	return ServiceEvent{}, ctx.Err()
}

func TestSupervisorCancellationStopsEventWait(t *testing.T) {
	t.Parallel()
	source := &blockingEventSource{ready: make(chan struct{})}
	supervisor := Supervisor{Source: source, Observer: fixedEventObserver{}, Sink: &collectingSink{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	<-source.ready
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}
