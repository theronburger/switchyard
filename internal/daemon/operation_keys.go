package daemon

import (
	"context"
	"sort"
	"sync"
)

// OperationKeys serializes the execution of operations that address the same
// resource while leaving unrelated resources concurrent. Every operation
// names the opaque keys it mutates; keys are acquired in sorted order so two
// operations sharing several keys cannot deadlock, and an operation waiting
// for a key stays pending rather than running alongside its conflict.
//
// One instance is shared by the workspace, environment, and profile-action
// services so a worktree preparation, an archive, an environment start's
// workspace ensure, and a worktree-scoped command action all contend for the
// same worktree key.
type OperationKeys struct {
	mutex sync.Mutex
	locks map[string]*operationKey
}

type operationKey struct {
	holders int
	slot    chan struct{}
}

func NewOperationKeys() *OperationKeys {
	return &OperationKeys{locks: make(map[string]*operationKey)}
}

func worktreeOperationKey(worktreeID string) string {
	return "worktree:" + worktreeID
}

func repositoryOperationKey(repositoryID string) string {
	return "repository:" + repositoryID
}

// Acquire blocks until every named key is held or ctx ends. Empty names are
// ignored and duplicates collapse. The returned release must be called
// exactly once; it is safe to call when no keys were requested.
//
// Keys are taken all-or-nothing: an operation never waits for one key while
// holding another, so an archive queued behind its worktree's preparation
// does not also stall unrelated worktrees that merely share its repository.
func (keys *OperationKeys) Acquire(ctx context.Context, names ...string) (func(), error) {
	if keys == nil {
		return func() {}, nil
	}
	ordered := uniqueSortedKeys(names)
	for {
		held, blocked := keys.tryAcquire(ordered)
		if blocked == "" {
			var once sync.Once
			return func() { once.Do(func() { keys.unlockAll(held) }) }, nil
		}
		keys.unlockAll(held)
		// Wait for the contended key with nothing held, then retry the full
		// set. An uncontended set is taken immediately even when ctx has
		// already ended: the operation then fails inside its own action with
		// the lifecycle error rather than being refused free resources.
		slot := keys.reserve(blocked)
		select {
		case slot <- struct{}{}:
			keys.unlock(blocked)
		case <-ctx.Done():
			keys.abandon(blocked)
			return func() {}, ctx.Err()
		}
	}
}

func (keys *OperationKeys) tryAcquire(ordered []string) ([]string, string) {
	held := make([]string, 0, len(ordered))
	for _, name := range ordered {
		slot := keys.reserve(name)
		select {
		case slot <- struct{}{}:
			held = append(held, name)
		default:
			keys.abandon(name)
			return held, name
		}
	}
	return held, ""
}

func (keys *OperationKeys) unlockAll(held []string) {
	for index := len(held) - 1; index >= 0; index-- {
		keys.unlock(held[index])
	}
}

// reserve registers interest in a key so it survives until every holder and
// waiter has released it, and returns the single-slot channel guarding it.
func (keys *OperationKeys) reserve(name string) chan struct{} {
	keys.mutex.Lock()
	defer keys.mutex.Unlock()
	lock, exists := keys.locks[name]
	if !exists {
		lock = &operationKey{slot: make(chan struct{}, 1)}
		keys.locks[name] = lock
	}
	lock.holders++
	return lock.slot
}

// unlock frees a key the caller holds; abandon drops a waiter that never
// acquired the slot and therefore must not drain it.
func (keys *OperationKeys) unlock(name string) {
	keys.mutex.Lock()
	defer keys.mutex.Unlock()
	lock, exists := keys.locks[name]
	if !exists {
		return
	}
	<-lock.slot
	keys.forget(name, lock)
}

func (keys *OperationKeys) abandon(name string) {
	keys.mutex.Lock()
	defer keys.mutex.Unlock()
	if lock, exists := keys.locks[name]; exists {
		keys.forget(name, lock)
	}
}

func (keys *OperationKeys) forget(name string, lock *operationKey) {
	lock.holders--
	if lock.holders <= 0 {
		delete(keys.locks, name)
	}
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
