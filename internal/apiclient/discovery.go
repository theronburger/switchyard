package apiclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

const RuntimeDescriptorSchemaVersion = contractv2.SchemaVersion

const (
	maximumDescriptorBytes = 64 * 1024
	maximumTokenBytes      = 4 * 1024
	minimumTokenBytes      = 32
	maximumClockSkew       = time.Minute
)

type RuntimePaths struct {
	Descriptor string
	Token      string
}

type RuntimeDescriptor = contractv2.RuntimeDescriptor

type ProcessIdentityVerifier func(pid int, startedAt time.Time) error

type DiscoveryPolicy struct {
	Now                   func() time.Time
	MaximumDescriptorAge  time.Duration
	RequiredDaemonVersion string
	VerifyProcessIdentity ProcessIdentityVerifier
}

type Connection struct {
	descriptor RuntimeDescriptor
	endpoint   *url.URL
	token      string
}

func (c Connection) Descriptor() RuntimeDescriptor {
	return c.descriptor
}

func Discover(paths RuntimePaths, policy DiscoveryPolicy) (Connection, error) {
	descriptorContents, err := readPrivateFile(paths.Descriptor, maximumDescriptorBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Connection{}, newCodedError(ErrorRuntimeDescriptorUnavailable, err)
		}
		return Connection{}, newCodedError(ErrorRuntimeDescriptorUnsafe, err)
	}

	descriptor, endpoint, err := decodeDescriptor(descriptorContents, policy)
	if err != nil {
		return Connection{}, err
	}

	tokenContents, err := readPrivateFile(paths.Token, maximumTokenBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Connection{}, newCodedError(ErrorRuntimeTokenUnavailable, err)
		}
		return Connection{}, newCodedError(ErrorRuntimeTokenUnsafe, err)
	}
	token, err := decodeToken(tokenContents)
	if err != nil {
		return Connection{}, err
	}

	return Connection{descriptor: descriptor, endpoint: endpoint, token: token}, nil
}

func readPrivateFile(path string, maximumBytes int64) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is required")
	}

	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symbolic links are not accepted")
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not regular")
	}
	if linkInfo.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("permissions are %04o, want 0600", linkInfo.Mode().Perm())
	}
	if linkInfo.Size() > maximumBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maximumBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(linkInfo, openedInfo) {
		return nil, fmt.Errorf("file changed while opening")
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("opened file is not a private regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximumBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maximumBytes)
	}
	return contents, nil
}

func decodeDescriptor(contents []byte, policy DiscoveryPolicy) (RuntimeDescriptor, *url.URL, error) {
	var descriptor RuntimeDescriptor
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(&descriptor); err != nil {
		return RuntimeDescriptor{}, nil, newCodedError(ErrorRuntimeDescriptorInvalid, err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return RuntimeDescriptor{}, nil, newCodedError(ErrorRuntimeDescriptorInvalid, err)
	}
	if descriptor.DaemonInstanceID == "" || descriptor.DaemonVersion == "" || descriptor.PID <= 0 ||
		descriptor.ProcessStartedAt.IsZero() || descriptor.GeneratedAt.IsZero() || descriptor.SchemaVersion <= 0 {
		return RuntimeDescriptor{}, nil, newCodedError(
			ErrorRuntimeDescriptorInvalid,
			fmt.Errorf("required descriptor fields are missing or invalid"))
	}
	if descriptor.SchemaVersion != RuntimeDescriptorSchemaVersion {
		// A well-formed descriptor from another contract generation is an
		// upgrade problem, not a corrupt file: the client and daemon must be
		// brought to the same exact version before any request is made.
		return RuntimeDescriptor{}, nil, newCodedError(
			ErrorUpgradeRequired,
			fmt.Errorf("descriptor schema version %d does not match client schema version %d",
				descriptor.SchemaVersion, RuntimeDescriptorSchemaVersion))
	}

	now := time.Now()
	if policy.Now != nil {
		now = policy.Now()
	}
	if descriptor.ProcessStartedAt.After(descriptor.GeneratedAt.Add(maximumClockSkew)) ||
		descriptor.ProcessStartedAt.After(now.Add(maximumClockSkew)) ||
		descriptor.GeneratedAt.After(now.Add(maximumClockSkew)) {
		return RuntimeDescriptor{}, nil, newCodedError(
			ErrorRuntimeDescriptorStale,
			fmt.Errorf("descriptor timestamps are inconsistent"))
	}
	if policy.MaximumDescriptorAge > 0 && now.Sub(descriptor.GeneratedAt) > policy.MaximumDescriptorAge {
		return RuntimeDescriptor{}, nil, newCodedError(
			ErrorRuntimeDescriptorStale,
			fmt.Errorf("descriptor exceeds configured maximum age"))
	}
	if policy.RequiredDaemonVersion != "" && descriptor.DaemonVersion != policy.RequiredDaemonVersion {
		return RuntimeDescriptor{}, nil, newCodedError(
			ErrorRuntimeVersionMismatch,
			fmt.Errorf("daemon version does not match required version"))
	}
	if policy.VerifyProcessIdentity != nil {
		if err := policy.VerifyProcessIdentity(descriptor.PID, descriptor.ProcessStartedAt); err != nil {
			return RuntimeDescriptor{}, nil, newCodedError(ErrorRuntimeDescriptorStale, err)
		}
	}

	endpoint, err := validateEndpoint(descriptor.Endpoint)
	if err != nil {
		return RuntimeDescriptor{}, nil, newCodedError(ErrorRuntimeEndpointUnsafe, err)
	}
	return descriptor, endpoint, nil
}

func validateEndpoint(rawEndpoint string) (*url.URL, error) {
	endpoint, err := url.Parse(rawEndpoint)
	if err != nil {
		return nil, err
	}
	if endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" {
		return nil, fmt.Errorf("endpoint must use HTTP on 127.0.0.1")
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		endpoint.Path != "" || endpoint.RawPath != "" || endpoint.Opaque != "" {
		return nil, fmt.Errorf("endpoint must be an origin without credentials, path, query, or fragment")
	}
	if parsedIP := net.ParseIP(endpoint.Hostname()); parsedIP == nil || !parsedIP.IsLoopback() {
		return nil, fmt.Errorf("endpoint host is not a loopback IP")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("endpoint port is invalid")
	}
	if endpoint.Host != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) {
		return nil, fmt.Errorf("endpoint authority is not canonical")
	}
	return endpoint, nil
}

func decodeToken(contents []byte) (string, error) {
	token := strings.TrimSpace(string(contents))
	if token == "" || strings.IndexFunc(token, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) >= 0 {
		return "", newCodedError(ErrorRuntimeTokenInvalid, fmt.Errorf("token is empty or contains whitespace"))
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) < minimumTokenBytes {
		return "", newCodedError(ErrorRuntimeTokenInvalid, fmt.Errorf("token is not valid base64url entropy"))
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != token {
		return "", newCodedError(ErrorRuntimeTokenInvalid, fmt.Errorf("token is not canonically encoded"))
	}
	return token, nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON values are not accepted")
}
