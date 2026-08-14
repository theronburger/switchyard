package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

type ListenFunc func(network, address string) (net.Listener, error)

type LoopbackServer struct {
	listener   net.Listener
	httpServer *http.Server
	endpoint   string
}

func ListenLoopback(listen ListenFunc) (net.Listener, error) {
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	if err := validateLoopbackListener(listener); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func NewLoopbackServer(listener net.Listener, handler http.Handler) (*LoopbackServer, error) {
	if listener == nil {
		return nil, errors.New("listener is required")
	}
	if handler == nil {
		return nil, errors.New("http handler is required")
	}
	if err := validateLoopbackListener(listener); err != nil {
		return nil, err
	}

	address := listener.Addr().(*net.TCPAddr)
	endpoint := "http://" + net.JoinHostPort(address.IP.String(), fmt.Sprintf("%d", address.Port))
	return &LoopbackServer{
		listener: listener,
		endpoint: endpoint,
		httpServer: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 << 10,
		},
	}, nil
}

func (server *LoopbackServer) Endpoint() string {
	return server.endpoint
}

func (server *LoopbackServer) Serve() error {
	err := server.httpServer.Serve(server.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (server *LoopbackServer) Shutdown(ctx context.Context) error {
	return server.httpServer.Shutdown(ctx)
}

func validateLoopbackListener(listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.IP == nil || !address.IP.IsLoopback() {
		return errors.New("daemon listener must bind a loopback TCP address")
	}
	return nil
}
