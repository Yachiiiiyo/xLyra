package market

import (
	"context"
	"strings"
	"testing"
	"time"

	"xlyra/server/internal/marketproto"
)

func TestCloudURLUsesCachedVerifiedRoot(t *testing.T) {
	if _, err := (*Service)(nil).CloudURL(context.Background()); err == nil || !strings.Contains(err.Error(), "未缓存") {
		t.Fatalf("nil service: %v", err)
	}
	svc := &Service{}
	svc.trustCached = &cachedTrust{at: time.Now(), root: nil}
	if _, err := svc.CloudURL(context.Background()); err == nil || !strings.Contains(err.Error(), "未缓存") {
		t.Fatalf("empty cache: %v", err)
	}
	blank := "  "
	svc.trustCached = &cachedTrust{at: time.Now(), root: &marketproto.Root{Endpoints: marketproto.Endpoints{Cloud: &blank}}}
	if _, err := svc.CloudURL(context.Background()); err == nil || !strings.Contains(err.Error(), "未声明") {
		t.Fatalf("blank cloud: %v", err)
	}
	cloud := "https://cloud.example/root/"
	svc.trustCached = &cachedTrust{at: time.Now(), root: &marketproto.Root{Endpoints: marketproto.Endpoints{Cloud: &cloud}}}
	got, err := svc.CloudURL(context.Background())
	if err != nil || got != "https://cloud.example/root" {
		t.Fatalf("cloud = %q, err = %v", got, err)
	}
}
