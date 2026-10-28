package market

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"sync"

	"golang.org/x/sync/singleflight"

	"xlyra/server/internal/config"
	"xlyra/server/internal/jsplugin"
	"xlyra/server/internal/marketproto"
)

const (
	marketIconCacheLimit = 256
	maxMarketIconBytes   = 64 << 10
	marketIconURLPrefix  = "/api/v1/plugin-market/icons/"
)

var (
	ErrIconNotFound = errors.New("plugin market icon not found")
	ErrIconFailed   = errors.New("plugin market icon failed")
)

type iconBlob struct {
	sum  string
	body []byte
}

type iconCache struct {
	mu    sync.Mutex
	ll    *list.List
	index map[string]*list.Element
}

func newIconCache() *iconCache {
	return &iconCache{ll: list.New(), index: map[string]*list.Element{}}
}

func (c *iconCache) get(sum string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[sum]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return bytes.Clone(el.Value.(iconBlob).body), true
}

func (c *iconCache) put(sum string, body []byte) {
	if c == nil || sum == "" || len(body) == 0 || len(body) > maxMarketIconBytes {
		return
	}
	cloned := bytes.Clone(body)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[sum]; ok {
		c.ll.MoveToFront(el)
		el.Value = iconBlob{sum: sum, body: cloned}
		return
	}
	el := c.ll.PushFront(iconBlob{sum: sum, body: cloned})
	c.index[sum] = el
	for c.ll.Len() > marketIconCacheLimit {
		back := c.ll.Back()
		if back == nil {
			break
		}
		c.ll.Remove(back)
		delete(c.index, back.Value.(iconBlob).sum)
	}
}

type iconLoader struct {
	cache  *iconCache
	flight singleflight.Group
}

func newIconLoader() *iconLoader {
	return &iconLoader{cache: newIconCache()}
}

func (s *Service) Icon(ctx context.Context, sum string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !sha256Hex(sum) || s == nil || s.icons == nil {
		return nil, ErrIconNotFound
	}
	if !config.ReadPluginMarketConfig(s.conf).Enabled {
		return nil, ErrIconNotFound
	}
	snap, err := s.Snapshot(ctx)
	if err != nil || !snapshotHasIcon(snap, sum) {
		if err != nil {
			return nil, ErrIconFailed
		}
		return nil, ErrIconNotFound
	}
	if body, ok := s.icons.cache.get(sum); ok {
		return body, nil
	}
	v, err, _ := s.icons.flight.Do(sum, func() (any, error) {
		if body, ok := s.icons.cache.get(sum); ok {
			return body, nil
		}
		body, fetchErr := s.fetchMarketIcon(ctx, snap.Root, sum)
		if fetchErr != nil || len(body) == 0 || len(body) > maxMarketIconBytes {
			return nil, ErrIconFailed
		}
		s.icons.cache.put(sum, body)
		return body, nil
	})
	if err != nil {
		return nil, err
	}
	body, _ := v.([]byte)
	return bytes.Clone(body), nil
}

func (s *Service) fetchMarketIcon(ctx context.Context, root *marketproto.Root, sum string) ([]byte, error) {
	cfg := config.ReadPluginMarketConfig(s.conf)
	state, err := s.repo.GetMarketState(ctx)
	if err != nil {
		return nil, err
	}
	client, err := s.httpClient(cfg.ProxyID)
	if err != nil {
		return nil, err
	}
	return downloadRelative(ctx, client, s.installBases(cfg, state, root), "icons/"+sum+".png", maxMarketIconBytes, func(body []byte) error {
		if marketproto.FileSHA256(body) != sum {
			return errors.New("icon digest mismatch")
		}
		return jsplugin.ValidateIconBytes(body)
	})
}

func snapshotHasIcon(snap Snapshot, sum string) bool {
	if snap.Index == nil {
		return false
	}
	for _, plugin := range snap.Index.Plugins {
		if plugin.Icon != nil && plugin.Icon.SHA256 == sum {
			return true
		}
	}
	return false
}

func marketIconURL(icon *marketproto.Icon) string {
	if icon == nil || !sha256Hex(icon.SHA256) {
		return ""
	}
	return marketIconURLPrefix + icon.SHA256
}

func sha256Hex(sum string) bool {
	if len(sum) != 64 {
		return false
	}
	for i := 0; i < len(sum); i++ {
		c := sum[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
