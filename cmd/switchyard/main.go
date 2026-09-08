package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/theronburger/switchyard/internal/localapi"
	"github.com/theronburger/switchyard/internal/workspaces"
)

var version = "development"
var buildChannel = "development"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--supervise" {
		os.Exit(workspaces.Supervise())
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	options, err := parseArgs(args)
	if err != nil {
		return err
	}
	switch options.command {
	case "version":
		if options.json {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": version, "schemaVersion": 3})
		}
		fmt.Println(version)
		return nil
	case "daemon":
		return serve(options.root)
	case "mcp":
		return serveMCP(localapi.Client{Root: options.root}, os.Stdin, os.Stdout)
	default:
		return runCLI(options)
	}
}
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func serve(root string) error {
	directory := filepath.Join(root, "daemon")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return errors.New("Switchyard's helper is already running.")
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	engine, err := workspaces.New(filepath.Join(root, "config.json"))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	token := randomID() + randomID()
	if err = writeConnectionFile(directory, "token", []byte(token)); err != nil {
		_ = listener.Close()
		return err
	}
	descriptor := localapi.Descriptor{Version: 3, Endpoint: "http://" + listener.Addr().String(), PID: os.Getpid(), InstanceID: engine.InstanceID()}
	payload, _ := json.Marshal(descriptor)
	if err = writeConnectionFile(directory, "runtime.json", payload); err != nil {
		_ = listener.Close()
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Handler: localapi.Server{Engine: engine, Token: token}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	closeErr := engine.Close(shutdownCtx)
	_ = os.Remove(filepath.Join(directory, "runtime.json"))
	return errors.Join(err, closeErr)
}

func writeConnectionFile(directory, name string, data []byte) error {
	file, err := os.CreateTemp(directory, ".connection-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, name))
}
