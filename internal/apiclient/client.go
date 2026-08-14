package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const (
	maximumHandshakeBytes = 64 * 1024
	maximumStatusBytes    = 16 * 1024 * 1024
	defaultRequestTimeout = 5 * time.Second
)

type Handshake struct {
	SchemaVersion           int    `json:"schemaVersion"`
	DaemonInstanceID        string `json:"daemonInstanceId"`
	DaemonVersion           string `json:"daemonVersion"`
	SupportedSchemaVersions []int  `json:"supportedSchemaVersions"`
}

type ClientOptions struct {
	Transport             http.RoundTripper
	RequestTimeout        time.Duration
	RequiredDaemonVersion string
}

type Client struct {
	connection            Connection
	httpClient            *http.Client
	requiredDaemonVersion string
}

func NewClient(connection Connection, options ClientOptions) *Client {
	transport := options.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	timeout := options.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	requiredVersion := options.RequiredDaemonVersion
	if requiredVersion == "" {
		requiredVersion = connection.descriptor.DaemonVersion
	}
	return &Client{
		connection: connection,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		requiredDaemonVersion: requiredVersion,
	}
}

func (c *Client) Handshake(ctx context.Context) (Handshake, error) {
	var handshake Handshake
	if err := c.getJSON(ctx, "/handshake", maximumHandshakeBytes, &handshake); err != nil {
		return Handshake{}, err
	}
	if handshake.SchemaVersion != RuntimeDescriptorSchemaVersion ||
		handshake.DaemonInstanceID == "" || handshake.DaemonVersion == "" ||
		len(handshake.SupportedSchemaVersions) == 0 {
		return Handshake{}, newCodedError(
			ErrorDaemonResponseInvalid,
			fmt.Errorf("handshake is missing required fields"))
	}
	if handshake.DaemonInstanceID != c.connection.descriptor.DaemonInstanceID {
		return Handshake{}, newCodedError(
			ErrorDaemonUnknown,
			fmt.Errorf("daemon instance does not match runtime descriptor"))
	}
	if handshake.DaemonVersion != c.connection.descriptor.DaemonVersion ||
		handshake.DaemonVersion != c.requiredDaemonVersion {
		return Handshake{}, newCodedError(
			ErrorDaemonIncompatible,
			fmt.Errorf("daemon version does not match client"))
	}
	if !slices.Contains(handshake.SupportedSchemaVersions, contractv1.SchemaVersion) {
		return Handshake{}, newCodedError(
			ErrorDaemonIncompatible,
			fmt.Errorf("daemon does not support schema version %d", contractv1.SchemaVersion))
	}
	return handshake, nil
}

func (c *Client) Status(ctx context.Context) (contractv1.StatusSnapshot, error) {
	if _, err := c.Handshake(ctx); err != nil {
		return contractv1.StatusSnapshot{}, err
	}
	return c.statusAfterHandshake(ctx)
}

func (c *Client) statusAfterHandshake(ctx context.Context) (contractv1.StatusSnapshot, error) {
	var snapshot contractv1.StatusSnapshot
	if err := c.getJSON(ctx, "/v1/status", maximumStatusBytes, &snapshot); err != nil {
		return contractv1.StatusSnapshot{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return contractv1.StatusSnapshot{}, newCodedError(ErrorDaemonStatusInvalid, err)
	}
	if snapshot.Daemon.InstanceID != c.connection.descriptor.DaemonInstanceID ||
		snapshot.Daemon.Version != c.connection.descriptor.DaemonVersion {
		return contractv1.StatusSnapshot{}, newCodedError(
			ErrorDaemonUnknown,
			fmt.Errorf("status identity does not match runtime descriptor"))
	}
	return snapshot, nil
}

func (c *Client) getJSON(ctx context.Context, path string, maximumBytes int64, destination any) error {
	requestURL := *c.connection.endpoint
	requestURL.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return newCodedError(ErrorDaemonUnavailable, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.connection.token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return newCodedError(ErrorDaemonUnavailable, err)
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return newCodedError(ErrorDaemonUnauthorized, fmt.Errorf("daemon rejected authentication"))
	case http.StatusNotFound:
		return newCodedError(ErrorDaemonUnknown, fmt.Errorf("endpoint does not identify a Switchyard daemon"))
	default:
		return newCodedError(
			ErrorDaemonResponseInvalid,
			fmt.Errorf("daemon returned HTTP %d", response.StatusCode))
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Cache-Control")), "no-store") ||
		!strings.EqualFold(response.Header.Get("X-Content-Type-Options"), "nosniff") {
		return newCodedError(
			ErrorDaemonResponseInvalid,
			fmt.Errorf("daemon response is missing required security headers"))
	}

	contents, err := io.ReadAll(io.LimitReader(response.Body, maximumBytes+1))
	if err != nil {
		return newCodedError(ErrorDaemonResponseInvalid, err)
	}
	if int64(len(contents)) > maximumBytes {
		return newCodedError(ErrorDaemonResponseInvalid, fmt.Errorf("daemon response exceeds size limit"))
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(destination); err != nil {
		return newCodedError(ErrorDaemonResponseInvalid, err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return newCodedError(ErrorDaemonResponseInvalid, err)
	}
	return nil
}

type Connector struct {
	Paths           RuntimePaths
	DiscoveryPolicy DiscoveryPolicy
	ClientOptions   ClientOptions
}

func (c Connector) Client() (*Client, error) {
	connection, err := Discover(c.Paths, c.DiscoveryPolicy)
	if err != nil {
		return nil, err
	}
	return NewClient(connection, c.ClientOptions), nil
}

func (c Connector) Status(ctx context.Context) (contractv1.StatusSnapshot, error) {
	client, err := c.Client()
	if err != nil {
		return contractv1.StatusSnapshot{}, err
	}
	return client.Status(ctx)
}

func IsConnectionFailure(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || CodeOf(err) == ErrorDaemonUnavailable
}
