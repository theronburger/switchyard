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
	"strconv"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

const (
	maximumHandshakeBytes = 64 * 1024
	maximumStatusBytes    = 16 * 1024 * 1024
	defaultRequestTimeout = 5 * time.Second
)

type Handshake = contractv2.Handshake

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
	if handshake.DaemonInstanceID == "" || handshake.DaemonVersion == "" ||
		len(handshake.SupportedSchemaVersions) == 0 {
		return Handshake{}, newCodedError(
			ErrorDaemonResponseInvalid,
			fmt.Errorf("handshake is missing required fields"))
	}
	if handshake.SchemaVersion != contractv2.SchemaVersion {
		if handshake.SchemaVersion <= 0 {
			return Handshake{}, newCodedError(
				ErrorDaemonResponseInvalid,
				fmt.Errorf("handshake schema version is invalid"))
		}
		return Handshake{}, newCodedError(
			ErrorUpgradeRequired,
			fmt.Errorf("daemon speaks contract schema version %d, client requires %d", handshake.SchemaVersion, contractv2.SchemaVersion))
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
	if !slices.Contains(handshake.SupportedSchemaVersions, contractv2.SchemaVersion) {
		return Handshake{}, newCodedError(
			ErrorUpgradeRequired,
			fmt.Errorf("daemon does not support schema version %d", contractv2.SchemaVersion))
	}
	return handshake, nil
}

func (c *Client) Status(ctx context.Context) (contractv2.StatusSnapshot, error) {
	if _, err := c.Handshake(ctx); err != nil {
		return contractv2.StatusSnapshot{}, err
	}
	return c.statusAfterHandshake(ctx)
}

func (c *Client) statusAfterHandshake(ctx context.Context) (contractv2.StatusSnapshot, error) {
	var snapshot contractv2.StatusSnapshot
	if err := c.getJSON(ctx, "/v1/status", maximumStatusBytes, &snapshot); err != nil {
		return contractv2.StatusSnapshot{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return contractv2.StatusSnapshot{}, newCodedError(ErrorDaemonStatusInvalid, err)
	}
	if snapshot.Daemon.InstanceID != c.connection.descriptor.DaemonInstanceID ||
		snapshot.Daemon.Version != c.connection.descriptor.DaemonVersion {
		return contractv2.StatusSnapshot{}, newCodedError(
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
	c.declareContract(request)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return newCodedError(ErrorDaemonUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()

	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return newCodedError(ErrorDaemonUnauthorized, fmt.Errorf("daemon rejected authentication"))
	case http.StatusNotFound:
		return newCodedError(ErrorDaemonUnknown, fmt.Errorf("endpoint does not identify a Switchyard daemon"))
	case http.StatusUpgradeRequired:
		return upgradeRequiredError(response)
	default:
		return newCodedError(
			ErrorDaemonResponseInvalid,
			fmt.Errorf("daemon returned HTTP %d", response.StatusCode))
	}
	if !secureResponseHeaders(response) {
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

// declareContract authenticates a request and declares this client's exact
// contract schema version so the daemon can answer a mismatch with the stable
// UPGRADE_REQUIRED error instead of a generic validation failure.
func (c *Client) declareContract(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+c.connection.token)
	request.Header.Set(contractv2.SchemaVersionHeader, strconv.Itoa(contractv2.SchemaVersion))
}

// upgradeRequiredError maps an HTTP 426 answer to ErrorUpgradeRequired. The
// daemon's bounded error context is preserved when the envelope is readable;
// an unreadable envelope still reports the stable code because the status line
// alone is authoritative for the version mismatch.
func upgradeRequiredError(response *http.Response) error {
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximumHandshakeBytes+1))
	if err != nil || int64(len(contents)) > maximumHandshakeBytes {
		contents = nil
	}
	return upgradeRequiredFromContents(contents)
}

// upgradeRequiredFromContents maps an already-read HTTP 426 body. Every route
// helper uses it so that a mismatch is reported as ErrorUpgradeRequired
// whether or not the daemon's envelope was readable or from this generation.
func upgradeRequiredFromContents(contents []byte) error {
	var failure mutationErrorResponse
	if decodeSingleJSON(contents, &failure) == nil && failure.Error.Code == contractv2.UpgradeRequiredCode &&
		failure.Error.Message != "" {
		return newContractError(failure.Error, fmt.Errorf("daemon requires a different contract schema version"))
	}
	return newCodedError(ErrorUpgradeRequired, fmt.Errorf("daemon requires a different contract schema version"))
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

func (c Connector) Status(ctx context.Context) (contractv2.StatusSnapshot, error) {
	client, err := c.Client()
	if err != nil {
		return contractv2.StatusSnapshot{}, err
	}
	return client.Status(ctx)
}

func IsConnectionFailure(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || CodeOf(err) == ErrorDaemonUnavailable
}
