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

	connector := newConnector(paths)

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

func newConnector(paths applicationPaths) apiclient.Connector {
	return apiclient.Connector{
		Paths: apiclient.RuntimePaths{
			Descriptor: paths.runtimeDescriptor,
			Token:      paths.token,
		},
		DiscoveryPolicy: apiclient.DiscoveryPolicy{
			RequiredDaemonVersion: version,
			VerifyProcessIdentity: verifyProcessIdentity,
		},
		ClientOptions: apiclient.ClientOptions{RequiredDaemonVersion: version},
	}
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
	ctx, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if ctx.Err() != nil {
		return nil
	}

	store, err := state.Open(ctx, state.Config{Path: paths.database})
	if err != nil {
		return err
	}
	defer store.Close()

	instanceID, err := newInstanceID()
	if err != nil {
		return err
	}
	startedAt, err := processStartedAt(os.Getpid())
	if err != nil {
		return err
	}

	token, err := daemon.LoadOrCreateToken(paths.token, rand.Reader)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
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
	listenerOwned := true
	defer func() {
		if listenerOwned {
			_ = listener.Close()
		}
	}()
	server, err := daemon.NewLoopbackServer(listener, handler)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve()
	}()
	listenerOwned = false

	if err := publishInitialSnapshot(ctx, store, instanceID, startedAt); err != nil {
		if ctx.Err() != nil {
			return shutdownServerAndWait(server, serveErrors, nil)
		}
		return shutdownServerAndWait(server, serveErrors, err)
	}
	if ctx.Err() != nil {
		return shutdownServerAndWait(server, serveErrors, nil)
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
		return shutdownServerAndWait(server, serveErrors, err)
	}
	defer removeOwnedRuntimeDescriptor(paths.runtimeDescriptor, instanceID)

	select {
	case err := <-serveErrors:
		return shutdownServer(server, err)
	case <-ctx.Done():
		return shutdownServerAndWait(server, serveErrors, nil)
	}
}

func shutdownServer(server *daemon.LoopbackServer, cause error) error {
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return errors.Join(cause, server.Shutdown(shutdownContext))
}

func shutdownServerAndWait(server *daemon.LoopbackServer, serveErrors <-chan error, cause error) error {
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownContext)
	select {
	case serveErr := <-serveErrors:
		return errors.Join(cause, shutdownErr, serveErr)
	case <-shutdownContext.Done():
		return errors.Join(cause, shutdownErr, shutdownContext.Err())
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
	if snapshot.Repositories == nil {
		snapshot.Repositories = []contractv1.Repository{}
	}
	if snapshot.Environments == nil {
		snapshot.Environments = []contractv1.Environment{}
	}
	operations, err := store.ListOperations(ctx)
	if err != nil {
		return err
	}
	snapshot.Operations = operations
	if snapshot.Alerts == nil {
		snapshot.Alerts = []contractv1.Alert{}
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
