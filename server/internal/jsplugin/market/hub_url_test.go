package market

import (
	"context"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/marketproto"
)

func TestHubURLUsesCachedVerifiedRoot(t *testing.T) {
	if _, err := (*Service)(nil).HubURL(context.Background()); err == nil || !strings.Contains(err.Error(), "未缓存") {
		t.Fatalf("nil service: %v", err)
	}
	svc := &Service{}
	svc.trustCached = &cachedTrust{at: time.Now(), root: nil}
	if _, err := svc.HubURL(context.Background()); err == nil || !strings.Contains(err.Error(), "未缓存") {
		t.Fatalf("empty cache: %v", err)
	}
	blank := "  "
	svc.trustCached = &cachedTrust{at: time.Now(), root: &marketproto.Root{Endpoints: marketproto.Endpoints{Hub: &blank}}}
	if _, err := svc.HubURL(context.Background()); err == nil || !strings.Contains(err.Error(), "未声明") {
		t.Fatalf("blank hub: %v", err)
	}
	hub := "https://hub.example/root/"
	svc.trustCached = &cachedTrust{at: time.Now(), root: &marketproto.Root{Endpoints: marketproto.Endpoints{Hub: &hub}}}
	got, err := svc.HubURL(context.Background())
	if err != nil || got != "https://hub.example/root" {
		t.Fatalf("hub = %q, err = %v", got, err)
	}
}
