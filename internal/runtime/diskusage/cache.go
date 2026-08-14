package diskusage

import (
	"container/list"
	"errors"
	"sync"
)

const (
	DefaultCacheCapacity = 64
	MaximumCacheCapacity = 1024
)

var ErrInvalidCacheCapacity = errors.New("invalid disk usage cache capacity")

type Cache struct {
	mutex    sync.Mutex
	capacity int
	recent   *list.List
	byKey    map[string]*list.Element
}

type cacheEntry struct {
	key      string
	revision uint64
	report   Report
}

func NewCache(capacity int) (*Cache, error) {
	if capacity == 0 {
		capacity = DefaultCacheCapacity
	}
	if capacity < 1 || capacity > MaximumCacheCapacity {
		return nil, ErrInvalidCacheCapacity
	}
	return &Cache{capacity: capacity, recent: list.New(), byKey: make(map[string]*list.Element)}, nil
}

func (cache *Cache) Get(key string, revision uint64) (Report, bool) {
	if key == "" {
		return Report{}, false
	}
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	element, exists := cache.byKey[key]
	if !exists {
		return Report{}, false
	}
	entry := element.Value.(cacheEntry)
	if entry.revision != revision {
		if revision > entry.revision {
			cache.remove(element)
		}
		return Report{}, false
	}
	cache.recent.MoveToFront(element)
	return cloneReport(entry.report), true
}

func (cache *Cache) Put(key string, revision uint64, report Report) bool {
	if key == "" {
		return false
	}
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if existing, exists := cache.byKey[key]; exists {
		current := existing.Value.(cacheEntry)
		if revision < current.revision {
			return false
		}
		existing.Value = cacheEntry{key: key, revision: revision, report: cloneReport(report)}
		cache.recent.MoveToFront(existing)
		return true
	}
	element := cache.recent.PushFront(cacheEntry{key: key, revision: revision, report: cloneReport(report)})
	cache.byKey[key] = element
	if cache.recent.Len() > cache.capacity {
		cache.remove(cache.recent.Back())
	}
	return true
}

func (cache *Cache) Invalidate(key string) bool {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	element, exists := cache.byKey[key]
	if !exists {
		return false
	}
	cache.remove(element)
	return true
}

func (cache *Cache) InvalidateRevision(key string, revision uint64) bool {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	element, exists := cache.byKey[key]
	if !exists || element.Value.(cacheEntry).revision != revision {
		return false
	}
	cache.remove(element)
	return true
}

func (cache *Cache) InvalidateAll() {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.recent.Init()
	clear(cache.byKey)
}

func (cache *Cache) Len() int {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	return cache.recent.Len()
}

func (cache *Cache) remove(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(cacheEntry)
	delete(cache.byKey, entry.key)
	cache.recent.Remove(element)
}

func cloneReport(report Report) Report {
	report.Reasons = append([]PartialReason(nil), report.Reasons...)
	return report
}
