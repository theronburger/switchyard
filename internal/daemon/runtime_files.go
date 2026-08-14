package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const tokenByteCount = 32

var ErrInsecureRuntimeFile = errors.New("runtime file permissions are not private")

type RuntimeDescriptor struct {
	SchemaVersion    int       `json:"schemaVersion"`
	Endpoint         string    `json:"endpoint"`
	DaemonInstanceID string    `json:"daemonInstanceId"`
	DaemonVersion    string    `json:"daemonVersion"`
	PID              int       `json:"pid"`
	ProcessStartedAt time.Time `json:"processStartedAt"`
	GeneratedAt      time.Time `json:"generatedAt"`
}

func LoadOrCreateToken(path string, random io.Reader) (string, error) {
	if path == "" {
		return "", errors.New("token path is required")
	}
	if random == nil {
		random = rand.Reader
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create token directory: %w", err)
	}

	contents, err := os.ReadFile(path)
	if err == nil {
		return validateTokenFile(path, contents)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read daemon token: %w", err)
	}

	tokenBytes := make([]byte, tokenByteCount)
	if _, err := io.ReadFull(random, tokenBytes); err != nil {
		return "", fmt.Errorf("generate daemon token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read concurrently created daemon token: %w", readErr)
		}
		return validateTokenFile(path, contents)
	}
	if err != nil {
		return "", fmt.Errorf("create daemon token: %w", err)
	}

	removeIncomplete := true
	defer func() {
		if removeIncomplete {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("protect daemon token: %w", err)
	}
	if _, err := io.WriteString(file, token+"\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write daemon token: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync daemon token: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close daemon token: %w", err)
	}
	removeIncomplete = false
	return token, nil
}

func validateTokenFile(path string, contents []byte) (string, error) {
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect daemon token: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		return "", errors.New("daemon token is not a regular file")
	}
	if fileInfo.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("%w: token mode is %04o, want 0600", ErrInsecureRuntimeFile, fileInfo.Mode().Perm())
	}

	token := strings.TrimSpace(string(contents))
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != tokenByteCount || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return "", errors.New("daemon token is not a canonical 256-bit base64url value")
	}
	return token, nil
}

func PublishRuntimeDescriptor(path string, descriptor RuntimeDescriptor) error {
	if err := validateRuntimeDescriptor(descriptor); err != nil {
		return err
	}
	if path == "" {
		return errors.New("runtime descriptor path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create runtime directory: %w", err)
	}

	payload, err := json.Marshal(descriptor)
	if err != nil {
		return fmt.Errorf("encode runtime descriptor: %w", err)
	}
	payload = append(payload, '\n')
	if err := writePrivateFileAtomically(path, payload); err != nil {
		return fmt.Errorf("publish runtime descriptor: %w", err)
	}
	return nil
}

func validateRuntimeDescriptor(descriptor RuntimeDescriptor) error {
	if descriptor.SchemaVersion <= 0 {
		return errors.New("runtime descriptor schema version is required")
	}
	if descriptor.DaemonInstanceID == "" {
		return errors.New("daemon instance id is required")
	}
	if descriptor.DaemonVersion == "" {
		return errors.New("daemon version is required")
	}
	if descriptor.PID <= 0 {
		return errors.New("daemon pid must be positive")
	}
	if descriptor.ProcessStartedAt.IsZero() || descriptor.GeneratedAt.IsZero() {
		return errors.New("runtime descriptor times are required")
	}

	parsedEndpoint, err := url.Parse(descriptor.Endpoint)
	if err != nil {
		return fmt.Errorf("parse daemon endpoint: %w", err)
	}
	endpointIP := net.ParseIP(parsedEndpoint.Hostname())
	if parsedEndpoint.Scheme != "http" || endpointIP == nil || !endpointIP.IsLoopback() {
		return errors.New("daemon endpoint must be loopback HTTP")
	}
	if parsedEndpoint.User != nil || parsedEndpoint.RawQuery != "" || parsedEndpoint.Fragment != "" {
		return errors.New("daemon endpoint must not contain credentials, query, or fragment")
	}
	if parsedEndpoint.Path != "" {
		return errors.New("daemon endpoint must not contain a path")
	}
	port, err := strconv.Atoi(parsedEndpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("daemon endpoint port is required")
	}
	return nil
}

func writePrivateFileAtomically(path string, payload []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
