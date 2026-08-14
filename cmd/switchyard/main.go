package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	"github.com/theronburger/switchyard/internal/cli"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/mcp"
	"github.com/theronburger/switchyard/internal/state"
)

const version = "0.1.0-dev"

const applicationSupportOverride = "SWITCHYARD_APPLICATION_SUPPORT"

func main() {
	os.Exit(run(context.Background(), os.Args[1:]))
}

func run(ctx context.Context, arguments []string) int {
	if len(arguments) == 1 && arguments[0] == "version" {
		return writeVersion()
	}

	paths, err := localPaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Switchyard could not resolve its local application directory.")
		return cli.ExitFailure
	}

	connector := apiclient.Connector{
		Paths: apiclient.RuntimePaths{
			Descriptor: paths.runtimeDescriptor,
			Token:      paths.token,
		},
		DiscoveryPolicy: apiclient.DiscoveryPolicy{RequiredDaemonVersion: version},
		ClientOptions:   apiclient.ClientOptions{RequiredDaemonVersion: version},
	}

	if len(arguments) == 1 && arguments[0] == "daemon" {
		if err := runDaemon(ctx, paths); err != nil {
			fmt.Fprintln(os.Stderr, "Switchyard daemon stopped with an error.")
			return cli.ExitFailure
		}
		return cli.ExitSuccess
	}
	if len(arguments) == 1 && arguments[0] == "mcp" {
		server := mcp.Server{
			Backend: mcp.LiveBackend{Connector: connector},
			Name:    "switchyard",
			Version: version,
		}
		if err := server.Run(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "Switchyard MCP server stopped with an error.")
			return cli.ExitFailure
		}
		return cli.ExitSuccess
	}

	application := cli.Application{
		Backend: cli.LiveBackend{Connector: connector},
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	return application.Run(ctx, arguments)
}

func writeVersion() int {
	err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schemaVersion": contractv1.SchemaVersion,
		"version":       version,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Switchyard could not write its version.")
		return cli.ExitFailure
	}
	return cli.ExitSuccess
}

type applicationPaths struct {
	directory         string
	database          string
	runtimeDescriptor string
	token             string
}

func localPaths() (applicationPaths, error) {
	configurationDirectory := os.Getenv(applicationSupportOverride)
	if configurationDirectory == "" {
		var err error
		configurationDirectory, err = os.UserConfigDir()
		if err != nil {
			return applicationPaths{}, err
		}
	}
	directory := filepath.Join(configurationDirectory, "Switchyard", "daemon")
	return applicationPaths{
		directory:         directory,
		database:          filepath.Join(directory, "state.sqlite"),
		runtimeDescriptor: filepath.Join(directory, "runtime.json"),
		token:             filepath.Join(directory, "token"),
	}, nil
}

func runDaemon(parent context.Context, paths applicationPaths) error {
	store, err := state.Open(parent, state.Config{Path: paths.database})
	if err != nil {
		return err
	}
	defer store.Close()

	instanceID, err := newInstanceID()
	if err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	if err := publishInitialSnapshot(parent, store, instanceID, startedAt); err != nil {
		return err
	}

	token, err := daemon.LoadOrCreateToken(paths.token, rand.Reader)
	if err != nil {
		return err
	}
	handler, err := daemon.NewHTTPHandler(daemon.HandlerConfig{
		Token:            token,
		DaemonInstanceID: instanceID,
		DaemonVersion:    version,
		StartedAt:        startedAt,
		StatusSource:     store,
	})
	if err != nil {
		return err
	}
	listener, err := daemon.ListenLoopback(nil)
	if err != nil {
		return err
	}
	server, err := daemon.NewLoopbackServer(listener, handler)
	if err != nil {
		return err
	}

	descriptor := contractv1.RuntimeDescriptor{
		SchemaVersion:    contractv1.SchemaVersion,
		Endpoint:         server.Endpoint(),
		DaemonInstanceID: instanceID,
		DaemonVersion:    version,
		PID:              os.Getpid(),
		ProcessStartedAt: startedAt,
		GeneratedAt:      time.Now().UTC(),
	}
	if err := daemon.PublishRuntimeDescriptor(paths.runtimeDescriptor, descriptor); err != nil {
		return err
	}
	defer removeOwnedRuntimeDescriptor(paths.runtimeDescriptor, instanceID)

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve()
	}()

	signalContext, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	select {
	case err := <-serveErrors:
		return err
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func removeOwnedRuntimeDescriptor(path string, instanceID string) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var descriptor contractv1.RuntimeDescriptor
	if json.Unmarshal(contents, &descriptor) != nil || descriptor.DaemonInstanceID != instanceID {
		return
	}
	_ = os.Remove(path)
}

func publishInitialSnapshot(
	ctx context.Context,
	store *state.Store,
	instanceID string,
	startedAt time.Time,
) error {
	snapshot, err := store.ReadSnapshot(ctx)
	if errors.Is(err, state.ErrNoSnapshot) {
		snapshot = contractv1.StatusSnapshot{}
	} else if err != nil {
		return err
	}
	snapshot.Daemon = contractv1.DaemonStatus{
		InstanceID: instanceID,
		Version:    version,
		State:      "ready",
		StartedAt:  startedAt,
	}
	_, err = store.CommitSnapshot(ctx, snapshot)
	return err
}

func newInstanceID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return "daemon_" + base64.RawURLEncoding.EncodeToString(randomBytes), nil
}
