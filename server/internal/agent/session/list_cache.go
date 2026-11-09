package session

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type listCacheEntry struct {
	metaModNano int64
	metaSize    int64
	piPath      string
	piModNano   int64
	piSize      int64
	updatedAt   time.Time
	item        ListItem
}

type listCache struct {
	mu      sync.Mutex
	entries map[string]listCacheEntry
}

var listCaches sync.Map
var listReadMeta = readMeta

func (s *Store) listItemsCached(running func(id string) bool) ([]ListItem, error) {
	value, _ := listCaches.LoadOrStore(s.root, &listCache{})
	cache := value.(*listCache)
	cache.mu.Lock()

	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cache.entries = map[string]listCacheEntry{}
			cache.mu.Unlock()
			return []ListItem{}, nil
		}
		cache.mu.Unlock()
		return nil, err
	}

	next := make(map[string]listCacheEntry, len(entries))
	ordered := make([]listCacheEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		id := entry.Name()
		metaPath := filepath.Join(s.root, id, metaFile)
		metaInfo, err := os.Stat(metaPath)
		if err != nil {
			continue
		}
		cached, ok := cache.entries[id]
		if ok && listCacheHit(cached, metaInfo) {
			next[id] = cached
			ordered = append(ordered, cached)
			continue
		}

		meta, err := listReadMeta(metaPath)
		if err != nil {
			continue
		}
		item := ListItem{
			SessionID: meta.ID,
			UpdatedAt: meta.UpdatedAt.UnixMilli(),
		}
		if meta.Title != "" {
			item.Title = meta.Title
		} else {
			item.Title = TitleFrom(firstUserText(meta))
		}
		item.Preview = s.Preview(meta)
		item.SessionAllow = append([]string{}, meta.SessionAllow...)

		cached = listCacheEntry{
			metaModNano: metaInfo.ModTime().UnixNano(),
			metaSize:    metaInfo.Size(),
			updatedAt:   meta.UpdatedAt,
			item:        item,
		}
		piPath, err := PiSessionFile(s.PiDir(meta.ID), meta.PiSessionID)
		if err == nil {
			cached.piPath = piPath
			if piInfo, statErr := os.Stat(piPath); statErr == nil {
				cached.piModNano = piInfo.ModTime().UnixNano()
				cached.piSize = piInfo.Size()
			}
		}
		next[id] = cached
		ordered = append(ordered, cached)
	}
	cache.entries = next
	cache.mu.Unlock()

	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].updatedAt.Equal(ordered[j].updatedAt) {
			return ordered[i].updatedAt.After(ordered[j].updatedAt)
		}
		return ordered[i].item.SessionID < ordered[j].item.SessionID
	})
	items := make([]ListItem, 0, len(ordered))
	for _, entry := range ordered {
		item := entry.item
		item.SessionAllow = append([]string{}, entry.item.SessionAllow...)
		if running != nil {
			item.Running = running(item.SessionID)
		}
		items = append(items, item)
	}
	return items, nil
}

func listCacheHit(cached listCacheEntry, metaInfo os.FileInfo) bool {
	if cached.metaModNano != metaInfo.ModTime().UnixNano() || cached.metaSize != metaInfo.Size() {
		return false
	}
	if cached.piPath == "" {
		return true
	}
	piInfo, err := os.Stat(cached.piPath)
	return err == nil && cached.piModNano == piInfo.ModTime().UnixNano() && cached.piSize == piInfo.Size()
}
