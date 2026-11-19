package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"xlyra/server/internal/agent/translate"
	"xlyra/server/internal/cloudclient"
)

type recordingSender struct {
	calls int
}

func (r *recordingSender) SendPush(context.Context, cloudclient.PushRequest) (cloudclient.PushResult, error) {
	r.calls++
	return cloudclient.PushOK, nil
}

func TestPushDisabledDoesNotCreateNotifier(t *testing.T) {
	cfg := testConfig()
	cfg.AgentDataDir = t.TempDir()
	cfg.PushEnabled = false
	builtin := newBuiltinAgent(cfg, slog.Default(), nil, nil, fakeBuiltinTokenIssuer{}, nil)
	if builtin.notifier != nil {
		t.Fatal("PUSH_ENABLED=false 时不应创建 Notifier")
	}
	builtin.startPush(context.Background(), nil, nil)
}

func TestPushEnabledBeforeSenderDrops(t *testing.T) {
	cfg := testConfig()
	cfg.AgentDataDir = t.TempDir()
	cfg.PushEnabled = true
	builtin := newBuiltinAgent(cfg, slog.New(slog.DiscardHandler), nil, nil, fakeBuiltinTokenIssuer{}, nil)
	if builtin.notifier == nil {
		t.Fatal("PUSH_ENABLED 缺省开启时应创建 Notifier")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	builtin.startPush(ctx, nil, nil)
	builtin.notifier.Observe("s1", "run-1", time.Now().Add(-time.Minute), translate.Event{Type: "agent_done"})
	time.Sleep(30 * time.Millisecond)
	sender := &recordingSender{}
	builtin.notifier.SetSender(sender)
	time.Sleep(30 * time.Millisecond)
	if sender.calls != 0 {
		t.Fatalf("SetSender 之前的通知应已丢弃,实际发送 %d", sender.calls)
	}
}
