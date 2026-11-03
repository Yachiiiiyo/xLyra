package runner

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"xlyra/server/internal/agent/session"
)

func TestForgetSessionWithoutIdentityOrSlot(t *testing.T) {
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{name: "Isolation nil", cfg: Config{Store: store}},
		{name: "没有槽位", cfg: Config{Store: store, Isolation: &IsolationConfig{}}},
		{name: "元数据不存在", cfg: Config{Store: store, Isolation: &IsolationConfig{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{cfg: tc.cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			id := meta.ID
			if tc.name == "元数据不存在" {
				id = "00000000-0000-0000-0000-000000000001"
			}
			if err := r.ForgetSession(id); err != nil {
				t.Fatalf("ForgetSession: %v", err)
			}
		})
	}
}

func TestForgetSessionWithSlot(t *testing.T) {
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	meta, err := store.Create("m", "ask")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(meta.ID, func(m *session.Meta) error {
		m.Slot = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{cfg: Config{Store: store, Isolation: &IsolationConfig{}}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := r.ForgetSession(meta.ID); err != nil {
		t.Fatalf("ForgetSession: %v", err)
	}
}
