package workspaces

import (
	"errors"
	"net"
	"strconv"
	"sync"
)

type portPool struct {
	mu   sync.Mutex
	held map[int]string
}

func (pool *portPool) reserve(runID string, preferred int) (int, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.held == nil {
		pool.held = make(map[int]string)
	}
	for attempts := 0; attempts < 32; attempts++ {
		port := 0
		if attempts == 0 && preferred > 0 && pool.held[preferred] == "" {
			port = preferred
		}
		listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		port = listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		if pool.held[port] != "" {
			continue
		}
		pool.held[port] = runID
		return port, nil
	}
	return 0, errors.New("could not find a free local port")
}

func (pool *portPool) release(runID string) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for port, heldBy := range pool.held {
		if heldBy == runID {
			delete(pool.held, port)
		}
	}
}
