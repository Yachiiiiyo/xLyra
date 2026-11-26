package textmatch

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"sync"
)

// DefaultCacheBytes is the default memory budget of a SegmentCache.
const DefaultCacheBytes = 64 << 20

const (
	cacheShards        = 16
	cacheEntryOverhead = 96
	cacheSpanOverhead  = 48
)

// CachedSpan is a finding kept by the cache: offsets and labels, never text.
type CachedSpan struct {
	Start  int
	End    int
	Type   string
	Source string
	Score  float64
}

// SegmentCache remembers the final findings of a segment. Agent clients send
// the whole history every turn, so most segments of a request were already
// inspected. The key is a hash of the text plus everything a plugin may judge
// by; the value holds no text.
type SegmentCache struct {
	shards [cacheShards]cacheShard
}

type cacheShard struct {
	mu    sync.Mutex
	limit int
	used  int
	order *list.List
	items map[string]*list.Element
}

type cacheEntry struct {
	key   string
	spans []CachedSpan
	size  int
}

// NewSegmentCache makes a cache that holds about maxBytes of entries.
func NewSegmentCache(maxBytes int) *SegmentCache {
	if maxBytes <= 0 {
		maxBytes = DefaultCacheBytes
	}
	cache := &SegmentCache{}
	for i := range cache.shards {
		cache.shards[i] = cacheShard{limit: maxBytes / cacheShards, order: list.New(), items: map[string]*list.Element{}}
	}
	return cache
}

// CacheKey identifies a segment: sha256 of its text, its role and field, and
// the binding's config hash. Role and field are part of the key because a
// hook may judge the same text differently in a tool result and a user turn.
func CacheKey(text, role, field, configHash string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:]) + "\x00" + role + "\x00" + field + "\x00" + configHash
}

func (c *SegmentCache) shard(key string) *cacheShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return &c.shards[h.Sum32()%cacheShards]
}

// Get returns a copy of the spans stored under key.
func (c *SegmentCache) Get(key string) ([]CachedSpan, bool) {
	if c == nil {
		return nil, false
	}
	shard := c.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	element, ok := shard.items[key]
	if !ok {
		return nil, false
	}
	shard.order.MoveToFront(element)
	entry := element.Value.(*cacheEntry)
	return append([]CachedSpan(nil), entry.spans...), true
}

// Put stores spans under key, evicting the least recently used entries of the
// shard to stay within its budget. An entry larger than the shard is dropped.
func (c *SegmentCache) Put(key string, spans []CachedSpan) {
	if c == nil {
		return
	}
	size := cacheEntryOverhead + len(key)
	for _, span := range spans {
		size += cacheSpanOverhead + len(span.Type) + len(span.Source)
	}
	shard := c.shard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if size > shard.limit {
		return
	}
	if element, ok := shard.items[key]; ok {
		entry := element.Value.(*cacheEntry)
		shard.used -= entry.size
		entry.spans = append([]CachedSpan(nil), spans...)
		entry.size = size
		shard.used += size
		shard.order.MoveToFront(element)
	} else {
		entry := &cacheEntry{key: key, spans: append([]CachedSpan(nil), spans...), size: size}
		shard.items[key] = shard.order.PushFront(entry)
		shard.used += size
	}
	for shard.used > shard.limit {
		oldest := shard.order.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*cacheEntry)
		shard.order.Remove(oldest)
		delete(shard.items, entry.key)
		shard.used -= entry.size
	}
}

// Len returns the number of entries, for tests and metrics.
func (c *SegmentCache) Len() int {
	if c == nil {
		return 0
	}
	total := 0
	for i := range c.shards {
		c.shards[i].mu.Lock()
		total += len(c.shards[i].items)
		c.shards[i].mu.Unlock()
	}
	return total
}

// Bytes returns the estimated memory in use.
func (c *SegmentCache) Bytes() int {
	if c == nil {
		return 0
	}
	total := 0
	for i := range c.shards {
		c.shards[i].mu.Lock()
		total += c.shards[i].used
		c.shards[i].mu.Unlock()
	}
	return total
}
