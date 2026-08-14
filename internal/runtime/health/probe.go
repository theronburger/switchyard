package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultProbeTimeout = 2 * time.Second
	HardMaxProbeTimeout = 10 * time.Second
	MaxProbePathBytes   = 2048
	MaxStatusRanges     = 16
	maxResponseHeaders  = 64 << 10
)

var ErrInvalidProbe = errors.New("invalid health probe")

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

type ProberConfig struct {
	DialContext DialContextFunc
	Now         func() time.Time
	MaxTimeout  time.Duration
}

type Prober struct {
	dial       DialContextFunc
	now        func() time.Time
	maxTimeout time.Duration
}

func NewProber(config ProberConfig) (*Prober, error) {
	if config.MaxTimeout == 0 {
		config.MaxTimeout = HardMaxProbeTimeout
	}
	if config.MaxTimeout <= 0 || config.MaxTimeout > HardMaxProbeTimeout {
		return nil, fmt.Errorf("%w: maximum timeout is outside the allowed range", ErrInvalidProbe)
	}
	if config.DialContext == nil {
		dialer := &net.Dialer{}
		config.DialContext = dialer.DialContext
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Prober{dial: config.DialContext, now: config.Now, maxTimeout: config.MaxTimeout}, nil
}

func (prober *Prober) Check(ctx context.Context, spec ProbeSpec) (ProbeResult, error) {
	observedAt := prober.now()
	result := ProbeResult{ProbeID: spec.ID, Kind: spec.Kind, Code: ResultInvalid, ObservedAt: observedAt}
	timeout, err := prober.validate(spec)
	if err != nil {
		return result, err
	}

	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	startedAt := prober.now()

	switch spec.Kind {
	case ProbeKindTCP:
		err = prober.checkTCP(probeContext, spec.Lease)
	case ProbeKindHTTP:
		result.Status, err = prober.checkHTTP(probeContext, spec)
	}
	result.Latency = prober.now().Sub(startedAt)
	if result.Latency < 0 {
		result.Latency = 0
	}
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return result, contextErr
		}
		if errors.Is(probeContext.Err(), context.DeadlineExceeded) || isTimeout(err) {
			result.Code = ResultTimeout
			return result, nil
		}
		result.Code = ResultUnavailable
		return result, nil
	}

	if spec.Kind == ProbeKindHTTP && !acceptsStatus(spec.AcceptedStatuses, result.Status) {
		result.Code = ResultUnexpectedStatus
		return result, nil
	}
	result.Success = true
	result.Code = ResultOK
	return result, nil
}

func (prober *Prober) validate(spec ProbeSpec) (time.Duration, error) {
	if spec.ID == "" {
		return 0, fmt.Errorf("%w: probe identifier is required", ErrInvalidProbe)
	}
	if spec.Lease.Host != LoopbackHost {
		return 0, fmt.Errorf("%w: lease must use literal IPv4 loopback", ErrInvalidProbe)
	}
	if spec.Lease.Port < 1 || spec.Lease.Port > 65535 {
		return 0, fmt.Errorf("%w: lease port is outside the allowed range", ErrInvalidProbe)
	}
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = DefaultProbeTimeout
	}
	if timeout <= 0 || timeout > prober.maxTimeout {
		return 0, fmt.Errorf("%w: timeout is outside the allowed range", ErrInvalidProbe)
	}

	switch spec.Kind {
	case ProbeKindTCP:
		if spec.Method != "" || spec.Path != "" || len(spec.AcceptedStatuses) != 0 {
			return 0, fmt.Errorf("%w: TCP probe contains HTTP fields", ErrInvalidProbe)
		}
	case ProbeKindHTTP:
		if spec.Method != http.MethodGet && spec.Method != http.MethodHead {
			return 0, fmt.Errorf("%w: HTTP method is not allowed", ErrInvalidProbe)
		}
		if !safePath(spec.Path) {
			return 0, fmt.Errorf("%w: HTTP path is not a local absolute path", ErrInvalidProbe)
		}
		if len(spec.AcceptedStatuses) == 0 || len(spec.AcceptedStatuses) > MaxStatusRanges {
			return 0, fmt.Errorf("%w: accepted HTTP statuses are required", ErrInvalidProbe)
		}
		for _, accepted := range spec.AcceptedStatuses {
			if accepted.Minimum < 100 || accepted.Maximum > 599 || accepted.Minimum > accepted.Maximum {
				return 0, fmt.Errorf("%w: accepted HTTP status range is invalid", ErrInvalidProbe)
			}
		}
	default:
		return 0, fmt.Errorf("%w: probe kind is not supported", ErrInvalidProbe)
	}
	return timeout, nil
}

func safePath(path string) bool {
	if path == "" || len(path) > MaxProbePathBytes || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return false
	}
	return !strings.ContainsAny(path, "?#@\r\n\t")
}

func (prober *Prober) checkTCP(ctx context.Context, lease Lease) error {
	address := net.JoinHostPort(lease.Host, strconv.Itoa(lease.Port))
	connection, err := prober.dial(ctx, "tcp4", address)
	if err != nil {
		return err
	}
	return connection.Close()
}

func (prober *Prober) checkHTTP(ctx context.Context, spec ProbeSpec) (int, error) {
	expectedAddress := net.JoinHostPort(spec.Lease.Host, strconv.Itoa(spec.Lease.Port))
	transport := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      false,
		MaxResponseHeaderBytes: maxResponseHeaders,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != expectedAddress {
				return nil, errors.New("health probe refused a non-leased destination")
			}
			return prober.dial(ctx, "tcp4", expectedAddress)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, spec.Method, "http://"+expectedAddress+spec.Path, nil)
	if err != nil {
		return 0, errors.New("could not build local health request")
	}
	request.Header.Set("User-Agent", "switchyard-health/1")
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

func acceptsStatus(ranges []StatusRange, status int) bool {
	for _, accepted := range ranges {
		if status >= accepted.Minimum && status <= accepted.Maximum {
			return true
		}
	}
	return false
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
