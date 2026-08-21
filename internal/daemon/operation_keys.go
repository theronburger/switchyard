package daemon

import (
	"context"
	"sort"
	"sync"
)

// OperationKeys serializes the execution of operations that address the same
// resource while leaving unrelated resources concurrent. Every operation
// names the opaque keys it mutates, and an operation waiting for a key stays
// pending rather than running alongside its conflict.
//
// One instance is shared by the workspace, environment, and profile-action
// services so a worktree preparation, an archive, an environment start's
// workspace ensure, and a worktree-scoped command action all contend for the
// same worktree key.
//
// Keys are granted all-or-nothing under one mutex from a first-come queue:
// an operation never holds one key while waiting for another, and every
// release or cancellation re-scans the queue oldest-first granting each
// waiter whose keys are neither held nor wanted by an older waiter still in
// the queue. A later waiter therefore cannot overtake an earlier one on a
// shared key, so a stream of repository-only operations cannot starve a
// queued worktree+repository archive, while two operations that share no key
// never wait on each other.
type OperationKeys struct {
	mutex   sync.Mutex
	held    map[string]struct{}
	waiters []*operationKeyWaiter
}

type operationKeyWaiter struct {
	keys                  []string
	granted               chan struct{}
	ctx                   context.Context
	cancellationSensitive bool
}

func NewOperationKeys() *OperationKeys {
	return &OperationKeys{held: make(map[string]struct{})}
}

func worktreeOperationKey(worktreeID string) string {
	return "worktree:" + worktreeID
}

func repositoryOperationKey(repositoryID string) string {
	return "repository:" + repositoryID
}

// Acquire blocks until every named key is held or ctx ends. Empty names are
// ignored and duplicates collapse. The returned release must be called
// exactly once; it is safe to call when no keys were requested and calling it
// again is a no-op.
//
// An uncontended operation takes its keys immediately even if ctx ended while
// it was being admitted. Once an operation has to wait, cancellation removes
// it without ever holding a key, including when cancellation races the current
// holder's release. A grant that linearizes before cancellation remains valid.
func (keys *OperationKeys) Acquire(ctx context.Context, names ...string) (func(), error) {
	ordered := uniqueSortedKeys(names)
	if keys == nil || len(ordered) == 0 {
		return func() {}, nil
	}
	waiter := &operationKeyWaiter{keys: ordered, granted: make(chan struct{}), ctx: ctx}
	keys.mutex.Lock()
	keys.waiters = append(keys.waiters, waiter)
	keys.grantInOrder()
	select {
	case <-waiter.granted:
	default:
		waiter.cancellationSensitive = true
	}
	keys.mutex.Unlock()

	select {
	case <-waiter.granted:
		return keys.releaser(ordered), nil
	case <-ctx.Done():
	}
	keys.mutex.Lock()
	defer keys.mutex.Unlock()
	select {
	case <-waiter.granted:
		// Granted concurrently with cancellation: the keys are held, so hand
		// them to the caller rather than leaking them.
		return keys.releaser(ordered), nil
	default:
	}
	keys.removeWaiter(waiter)
	keys.grantInOrder()
	return func() {}, ctx.Err()
}

func (keys *OperationKeys) releaser(ordered []string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			keys.mutex.Lock()
			defer keys.mutex.Unlock()
			for _, name := range ordered {
				delete(keys.held, name)
			}
			keys.grantInOrder()
		})
	}
}

// grantInOrder walks the queue from the oldest waiter and grants every waiter
// whose keys are free and not reserved by an older waiter left in the queue.
// Callers hold keys.mutex.
func (keys *OperationKeys) grantInOrder() {
	remaining := keys.waiters[:0]
	reserved := make(map[string]struct{})
	for _, waiter := range keys.waiters {
		if waiter.cancellationSensitive && waiter.ctx.Err() != nil {
			continue
		}
		if keys.grantable(waiter, reserved) {
			for _, name := range waiter.keys {
				keys.held[name] = struct{}{}
			}
			close(waiter.granted)
			continue
		}
		for _, name := range waiter.keys {
			reserved[name] = struct{}{}
		}
		remaining = append(remaining, waiter)
	}
	for index := len(remaining); index < len(keys.waiters); index++ {
		keys.waiters[index] = nil
	}
	keys.waiters = remaining
}

func (keys *OperationKeys) grantable(waiter *operationKeyWaiter, reserved map[string]struct{}) bool {
	for _, name := range waiter.keys {
		if _, held := keys.held[name]; held {
			return false
		}
		if _, wanted := reserved[name]; wanted {
			return false
		}
	}
	return true
}

func (keys *OperationKeys) removeWaiter(waiter *operationKeyWaiter) {
	for index, queued := range keys.waiters {
		if queued == waiter {
			keys.waiters = append(keys.waiters[:index], keys.waiters[index+1:]...)
			return
		}
	}
}

// idle reports whether no key is held and no operation is queued.
func (keys *OperationKeys) idle() bool {
	keys.mutex.Lock()
	defer keys.mutex.Unlock()
	return len(keys.held) == 0 && len(keys.waiters) == 0
}

func uniqueSortedKeys(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	ordered := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return ordered
}
