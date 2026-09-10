package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Descriptor struct {
	Version    int    `json:"version"`
	Endpoint   string `json:"endpoint"`
	PID        int    `json:"pid"`
	InstanceID string `json:"instanceId"`
}

type Client struct{ Root string }

var loopbackHTTPClient = http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (c Client) Request(ctx context.Context, method, path string, body, result any) error {
	timeout := 30 * time.Second
	gitMutation := method == http.MethodPost && (path == "/api/prune" || path == "/api/create")
	if gitMutation {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := os.ReadFile(filepath.Join(c.Root, "daemon", "runtime.json"))
	if err != nil {
		return errors.New("Switchyard is not running. Open the app to start its helper.")
	}
	var descriptor Descriptor
	if json.Unmarshal(raw, &descriptor) != nil || descriptor.Version != 3 {
		return errors.New("App and helper versions differ. Reopen Switchyard.")
	}
	endpoint, err := url.Parse(descriptor.Endpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("Invalid local helper address. Reopen Switchyard.")
	}
	if port, err := strconv.Atoi(endpoint.Port()); err != nil || port < 1 || port > 65535 {
		return errors.New("Invalid local helper port.")
	}
	token, err := os.ReadFile(filepath.Join(c.Root, "daemon", "token"))
	if err != nil {
		return errors.New("Cannot read the helper connection. Reopen Switchyard.")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, descriptor.Endpoint+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("X-Switchyard-Version", "3")
	request.Header.Set("Content-Type", "application/json")
	response, err := loopbackHTTPClient.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			if gitMutation {
				return errors.New("Stopped waiting for Git. The helper may still be finishing this action; refresh the worktree list before retrying.")
			}
			return errors.New("The request did not finish in time. Try again.")
		}
		return errors.New("Cannot reach Switchyard. Reopen the app to reconnect.")
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &failure)
		if failure.Error != "" {
			return errors.New(failure.Error)
		}
		return errors.New("The helper could not complete this action.")
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(payload, result)
}
