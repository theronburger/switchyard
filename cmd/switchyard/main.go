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
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/mcp"
	"github.com/theronburger/switchyard/internal/state"
)

var version = "0.1.0-dev"
var buildChannel = "development"

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
		"schemaVersion": contractv2.SchemaVersion,
		"version":       version,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Switchyard could not write its version.")
		return cli.ExitFailure
	}
	return cli.ExitSuccess
}

type applicationPaths struct {
	root              string
	directory         string
	database          string
	configuration     string
	runtimeDescriptor string
	token             string
}

// runtimeRoot is the one owner-only tree under which every runner writes
// process evidence, step run directories, action run directories, and
// managed-worktree ownership records, and from which cleanup planning and
// operation diagnostics read them. Writers and readers must never derive
// this path independently.
func (paths applicationPaths) runtimeRoot() string {
	return filepath.Join(paths.root, "runtime")
}

func (paths applicationPaths) cacheRoot() string {
	return filepath.Join(paths.root, "caches")
}

func newOperationDiagnosticsReader(store *state.Store, paths applicationPaths, runRoots daemon.EnvironmentRunRoots) (*daemon.OperationDiagnosticsReader, error) {
	return daemon.NewOperationDiagnosticsReader(store, paths.runtimeRoot(), runRoots)
}

func newCleanupService(store *state.Store, journal *state.WorkspaceJournal, paths applicationPaths) *daemon.CleanupService {
	return &daemon.CleanupService{Store: store, Workspaces: journal, RuntimeRoot: paths.runtimeRoot()}
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
	directoryName, err := applicationDirectoryName(buildChannel)
	if err != nil {
		return applicationPaths{}, err
	}
	root := filepath.Join(configurationDirectory, directoryName)
	directory := filepath.Join(root, "daemon")
	return applicationPaths{
		root:              root,
		directory:         directory,
		database:          filepath.Join(directory, "state-v2.sqlite"),
		configuration:     filepath.Join(root, "configuration.yaml"),
		runtimeDescriptor: filepath.Join(directory, "runtime.json"),
		token:             filepath.Join(directory, "token"),
	}, nil
}

func applicationDirectoryName(channel string) (string, error) {
	switch channel {
	case "release":
		return "Switchyard", nil
	case "development":
		return "Switchyard Development", nil
	default:
		return "", fmt.Errorf("unsupported build channel %q", channel)
	}
}

func runDaemon(parent context.Context, paths applicationPaths) error {
	signalContext, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, restartDaemon := context.WithCancel(signalContext)
	defer restartDaemon()
	if ctx.Err() != nil {
		return nil
	}

	store, err := state.Open(ctx, state.Config{Path: paths.database})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	instanceID, err := newInstanceID()
	if err != nil {
		return err
	}
	startedAt, err := processStartedAt(os.Getpid())
	if err != nil {
		return err
	}
	discoveredRepositories, err := discoverAcceptedRepositoryInventory(ctx, store, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := annotateWorkspaceInventory(paths, &discoveredRepositories); err != nil {
		return err
	}
	if err := restoreWorkspaceInventory(ctx, store, &discoveredRepositories); err != nil {
		return err
	}
	if err := restoreOccupancyInventory(ctx, store, &discoveredRepositories); err != nil {
		return err
	}
	if err := publishDaemonSnapshot(
		ctx, store, instanceID, startedAt, "starting", discoveredRepositories,
	); err != nil {
		return err
	}
	runtime, err := buildConfiguredProfileRuntime(
		ctx, store, paths, instanceID, discoveredRepositories, restartDaemon,
	)
	if err != nil {
		return err
	}
	if runtime.actions != nil || runtime.workspaceActions != nil || runtime.observerDone != nil {
		defer func() {
			waitContext, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			_ = runtime.CloseAndWait(waitContext)
		}()
	}
	if _, err := store.FailInterruptedOperations(ctx, contractv2.ContractError{
		Code:      "DAEMON_RESTARTED",
		Message:   "The daemon restarted before the operation completed.",
		Retryable: true,
	}); err != nil {
		return err
	}
	if err := publishDaemonSnapshot(
		ctx, store, instanceID, startedAt, "ready", discoveredRepositories,
	); err != nil {
		return err
	}

	token, err := daemon.LoadOrCreateToken(paths.token, rand.Reader)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	operationDiagnostics, err := newOperationDiagnosticsReader(store, paths, runtime.EnvironmentRunRoots())
	if err != nil {
		return err
	}
	cleanupJournal, err := state.NewWorkspaceJournal(store)
	if err != nil {
		return err
	}
	handler, err := daemon.NewHTTPHandler(daemon.HandlerConfig{
		Token:                token,
		DaemonInstanceID:     instanceID,
		DaemonVersion:        version,
		StartedAt:            startedAt,
		StatusSource:         store,
		EnvironmentActions:   runtime.actions,
		WorkspaceActions:     runtime.workspaceActions,
		ProfileActions:       runtime.profileActions,
		OperationDiagnostics: operationDiagnostics,
		Configuration: &daemon.ConfigurationService{
			Store: store, Path: paths.configuration, CompilerVersion: version, Restart: restartDaemon,
			References: store,
		},
		Cleanup:   newCleanupService(store, cleanupJournal, paths),
		Occupancy: &daemon.OccupancyService{Store: store},
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
	if ctx.Err() != nil {
		return shutdownServerAndWait(server, serveErrors, nil)
	}
	select {
	case serveErr := <-serveErrors:
		return shutdownServer(server, serveErr)
	default:
	}

	descriptor := contractv2.RuntimeDescriptor{
		SchemaVersion:    contractv2.SchemaVersion,
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
	githubObserverContext, stopGitHubObserver := context.WithCancel(ctx)
	githubObserverDone := make(chan struct{})
	go func() {
		defer close(githubObserverDone)
		_ = newGitHubStatusObserver(store).Run(githubObserverContext)
	}()
	defer func() {
		stopGitHubObserver()
		select {
		case <-githubObserverDone:
		case <-time.After(2 * time.Second):
		}
	}()
	repositoryObserverContext, stopRepositoryObserver := context.WithCancel(ctx)
	repositoryObserverDone := make(chan struct{})
	go func() {
		defer close(repositoryObserverDone)
		_ = newRepositoryObserver(store, paths, restartDaemon).Run(repositoryObserverContext)
	}()
	defer func() {
		stopRepositoryObserver()
		select {
		case <-repositoryObserverDone:
		case <-time.After(2 * time.Second):
		}
	}()

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
	var descriptor contractv2.RuntimeDescriptor
	if json.Unmarshal(contents, &descriptor) != nil || descriptor.DaemonInstanceID != instanceID {
		return
	}
	_ = os.Remove(path)
}

func publishDaemonSnapshot(
	ctx context.Context,
	store *state.Store,
	instanceID string,
	startedAt time.Time,
	daemonState string,
	discoveredRepositories repositoryInventory,
) error {
	snapshot, err := store.ReadSnapshot(ctx)
	if errors.Is(err, state.ErrNoSnapshot) {
		snapshot = contractv2.StatusSnapshot{}
	} else if err != nil {
		return err
	}
	snapshot.Daemon = contractv2.DaemonStatus{
		InstanceID: instanceID,
		Version:    version,
		State:      daemonState,
		StartedAt:  startedAt,
	}
	if snapshot.Repositories == nil {
		snapshot.Repositories = []contractv2.Repository{}
	}
	if snapshot.Environments == nil {
		snapshot.Environments = []contractv2.Environment{}
	}
	operations, err := store.ListOperations(ctx)
	if err != nil {
		return err
	}
	snapshot.Operations = operations
	if snapshot.Alerts == nil {
		snapshot.Alerts = []contractv2.Alert{}
	}
	snapshot = mergeRepositoryInventory(snapshot, discoveredRepositories)
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
