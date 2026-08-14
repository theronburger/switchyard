package health

import (
	"context"
	"errors"
	"io"
)

type ServiceEvent struct {
	Spec           ServiceSpec
	Previous       ServiceRuntimeState
	ProcessRunning bool
}

type EventSource interface {
	Next(ctx context.Context) (ServiceEvent, error)
}

type EventObserver interface {
	ObserveEvent(ctx context.Context, event ServiceEvent) (Observation, error)
}

type ObservationSink interface {
	Record(ctx context.Context, observation Observation) error
}

type Supervisor struct {
	Source   EventSource
	Observer EventObserver
	Sink     ObservationSink
}

func (supervisor Supervisor) Run(ctx context.Context) error {
	if supervisor.Source == nil || supervisor.Observer == nil || supervisor.Sink == nil {
		return errors.New("health supervisor dependencies are incomplete")
	}
	for {
		event, err := supervisor.Source.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		observation, err := supervisor.Observer.ObserveEvent(ctx, event)
		if err != nil {
			return err
		}
		if err := supervisor.Sink.Record(ctx, observation); err != nil {
			return err
		}
	}
}

func (monitor *Monitor) ObserveEvent(ctx context.Context, event ServiceEvent) (Observation, error) {
	return monitor.Observe(ctx, event.Spec, event.Previous, event.ProcessRunning)
}
