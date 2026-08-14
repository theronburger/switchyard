package diskusage

import (
	"fmt"
	"sync"
	"testing"
)

func TestCacheRequiresMatchingRevisionAndCopiesReports(t *testing.T) {
	t.Parallel()
	cache, err := NewCache(2)
	if err != nil {
		t.Fatal(err)
	}
	report := Report{Reasons: []PartialReason{ReasonEntryLimit}, Partial: true}
	if !cache.Put("worktree-1", 7, report) {
		t.Fatal("cache rejected valid key")
	}
	report.Reasons[0] = ReasonReadFailure
	cached, found := cache.Get("worktree-1", 7)
	if !found || cached.Reasons[0] != ReasonEntryLimit {
		t.Fatalf("cache did not isolate stored report: %+v", cached)
	}
	cached.Reasons[0] = ReasonTimeLimit
	again, found := cache.Get("worktree-1", 7)
	if !found || again.Reasons[0] != ReasonEntryLimit {
		t.Fatalf("cache did not isolate returned report: %+v", again)
	}
	if _, found := cache.Get("worktree-1", 8); found || cache.Len() != 0 {
		t.Fatal("stale revision remained cached")
	}
	cache.Put("worktree-1", 10, Report{EntriesVisited: 10})
	if _, found := cache.Get("worktree-1", 9); found || cache.Len() != 1 {
		t.Fatal("older reader evicted newer revision")
	}
	if cache.Put("worktree-1", 9, Report{}) {
		t.Fatal("older writer replaced newer revision")
	}
	if cache.InvalidateRevision("worktree-1", 9) || !cache.InvalidateRevision("worktree-1", 10) {
		t.Fatal("revision-scoped invalidation was not exact")
	}
}

func TestCacheIsBoundedLRUAndSupportsInvalidation(t *testing.T) {
	t.Parallel()
	cache, err := NewCache(2)
	if err != nil {
		t.Fatal(err)
	}
	cache.Put("a", 1, Report{})
	cache.Put("b", 1, Report{})
	if _, found := cache.Get("a", 1); !found {
		t.Fatal("expected a")
	}
	cache.Put("c", 1, Report{})
	if _, found := cache.Get("b", 1); found {
		t.Fatal("least recently used entry survived capacity eviction")
	}
	if !cache.Invalidate("a") || cache.Invalidate("a") {
		t.Fatal("single-key invalidation was not exact")
	}
	cache.InvalidateAll()
	if cache.Len() != 0 {
		t.Fatal("cache was not emptied")
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	t.Parallel()
	cache, err := NewCache(16)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for worker := range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			key := fmt.Sprintf("worktree-%d", worker%8)
			for revision := range 100 {
				cache.Put(key, uint64(revision), Report{EntriesVisited: uint64(revision)})
				cache.Get(key, uint64(revision))
				if revision%17 == 0 {
					cache.Invalidate(key)
				}
			}
		}()
	}
	group.Wait()
	if cache.Len() > 16 {
		t.Fatalf("cache exceeded capacity: %d", cache.Len())
	}
}
