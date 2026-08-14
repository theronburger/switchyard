package health

import (
	"context"
	"errors"
	"time"
)

const (
	DefaultBackoffInitial = 250 * time.Millisecond
	DefaultBackoffMaximum = 30 * time.Second
	DefaultCadenceMaximum = 5 * time.Minute
)

var ErrInvalidBackoff = errors.New("invalid health backoff")

type Backoff struct {
	Initial time.Duration
	Maximum time.Duration
}

func NewBackoff(initial, maximum time.Duration) (Backoff, error) {
	if initial <= 0 || maximum < initial {
		return Backoff{}, ErrInvalidBackoff
	}
	return Backoff{Initial: initial, Maximum: maximum}, nil
}

func DefaultBackoff() Backoff {
	return Backoff{Initial: DefaultBackoffInitial, Maximum: DefaultBackoffMaximum}
}

func (backoff Backoff) Delay(attempt uint) time.Duration {
	if attempt == 0 {
		return 0
	}
	if backoff.Initial <= 0 || backoff.Maximum < backoff.Initial {
		backoff = DefaultBackoff()
	}
	delay := backoff.Initial
	for current := uint(1); current < attempt; current++ {
		if delay >= backoff.Maximum || delay > backoff.Maximum/2 {
			return backoff.Maximum
		}
		delay *= 2
	}
	if delay > backoff.Maximum {
		return backoff.Maximum
	}
	return delay
}

func Wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type ThermalPressure string

const (
	ThermalNominal  ThermalPressure = "nominal"
	ThermalFair     ThermalPressure = "fair"
	ThermalSerious  ThermalPressure = "serious"
	ThermalCritical ThermalPressure = "critical"
)

type AdaptiveInputs struct {
	LowPowerMode    bool
	ThermalPressure ThermalPressure
	Essential       bool
}

type CadencePolicy struct {
	Starting  time.Duration
	Healthy   time.Duration
	Degraded  time.Duration
	Unhealthy time.Duration
	Maximum   time.Duration
}

func (policy CadencePolicy) Interval(state State, inputs AdaptiveInputs) time.Duration {
	base := policy.intervalForState(state)
	if base <= 0 {
		base = time.Second
	}
	maximum := policy.Maximum
	if maximum <= 0 {
		maximum = DefaultCadenceMaximum
	}
	if maximum < base {
		maximum = base
	}
	if inputs.Essential {
		return base
	}
	factor := uint(1)
	if inputs.LowPowerMode {
		factor *= 2
	}
	switch inputs.ThermalPressure {
	case ThermalSerious:
		factor *= 2
	case ThermalCritical:
		factor *= 4
	}
	return multiplyDuration(base, factor, maximum)
}

func (policy CadencePolicy) intervalForState(state State) time.Duration {
	switch state {
	case StateStarting:
		return policy.Starting
	case StateHealthy:
		return policy.Healthy
	case StateDegraded:
		return policy.Degraded
	case StateUnhealthy:
		return policy.Unhealthy
	default:
		return 0
	}
}

func multiplyDuration(value time.Duration, factor uint, maximum time.Duration) time.Duration {
	result := value
	for current := uint(1); current < factor; current++ {
		if result >= maximum || result > maximum-value {
			return maximum
		}
		result += value
	}
	return result
}
