package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"xlyra/server/internal/crypto"
)

func TestHubBindingEnsureIdentityIsStable(t *testing.T) {
	st := openHubBindingStore(t)
	repo := NewHubBindingRepository(st.DB())
	ctx := context.Background()
	first, err := repo.EnsureIdentity(ctx, generateHubIdentity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.EnsureIdentity(ctx, func() (string, string, error) {
		t.Fatal("generate called for an existing identity")
		return "", "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKey == "" || first.PublicKey != second.PublicKey || first.PrivateKeyEnc != second.PrivateKeyEnc {
		t.Fatalf("identity changed: %#v %#v", first, second)
	}

	var wg sync.WaitGroup
	keys := make([]string, 8)
	start := make(chan struct{})
	for i := 0; i < len(keys); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			row, err := repo.EnsureIdentity(ctx, generateHubIdentity)
			if err != nil {
				t.Errorf("ensure: %v", err)
				return
			}
			keys[i] = row.PublicKey
		}(i)
	}
	close(start)
	wg.Wait()
	for _, key := range keys {
		if key != first.PublicKey {
			t.Fatalf("concurrent key = %s, want %s", key, first.PublicKey)
		}
	}

	if err := repo.MarkBound(ctx, "inst-1", "from-cloud", "Ada", first.CreatedAt); err != nil {
		t.Fatal(err)
	}
	named, err := repo.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if named.InstanceName != "from-cloud" || named.UserDisplayName != "Ada" || named.InstanceID != "inst-1" {
		t.Fatalf("bound = %#v", named)
	}
	if err := repo.MarkBound(ctx, "inst-1", "", "Ada", first.CreatedAt); err != nil {
		t.Fatal(err)
	}
	kept, err := repo.Get(ctx)
	if err != nil || kept.InstanceName != "from-cloud" {
		t.Fatalf("empty name wiped the saved name: %#v err=%v", kept, err)
	}
	if err := repo.ClearBinding(ctx, HubStatusRevoked, "已在账号侧解除连接"); err != nil {
		t.Fatal(err)
	}
	cleared, err := repo.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Status != HubStatusRevoked || cleared.InstanceID != "" || cleared.PublicKey != first.PublicKey || cleared.PrivateKeyEnc != first.PrivateKeyEnc {
		t.Fatalf("cleared = %#v", cleared)
	}
}

func TestHubBindingStoresPairingCodeUntilBoundOrCleared(t *testing.T) {
	st := openHubBindingStore(t)
	repo := NewHubBindingRepository(st.DB())
	ctx := context.Background()
	if _, err := repo.EnsureIdentity(ctx, generateHubIdentity); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).UTC()
	const code = "K7QM-2XFD"
	if err := repo.SavePairing(ctx, "https://hub.example", "enc", "desk", code, expires); err != nil {
		t.Fatal(err)
	}
	row, err := repo.Get(ctx)
	if err != nil || row.Status != HubStatusPairing || row.PairingCodeHint != code {
		t.Fatalf("pairing = %#v err=%v", row, err)
	}
	if err := repo.ClearPairing(ctx, ""); err != nil {
		t.Fatal(err)
	}
	cleared, err := repo.Get(ctx)
	if err != nil || cleared.Status != HubStatusUnbound || cleared.PairingCodeHint != "" {
		t.Fatalf("clear pairing = %#v err=%v", cleared, err)
	}
	if err := repo.SavePairing(ctx, "https://hub.example", "enc", "desk", code, expires); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkBound(ctx, "inst-1", "desk", "Ada", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	bound, err := repo.Get(ctx)
	if err != nil || bound.Status != HubStatusBound || bound.PairingCodeHint != "" {
		t.Fatalf("bound hint = %q err=%v", bound.PairingCodeHint, err)
	}
	if err := repo.SavePairing(ctx, "https://hub.example", "enc", "desk", code, expires); err != nil {
		t.Fatal(err)
	}
	if err := repo.ClearBinding(ctx, HubStatusUnbound, ""); err != nil {
		t.Fatal(err)
	}
	unbound, err := repo.Get(ctx)
	if err != nil || unbound.PairingCodeHint != "" || unbound.Status != HubStatusUnbound {
		t.Fatalf("unbind hint = %q status=%s err=%v", unbound.PairingCodeHint, unbound.Status, err)
	}
}

func generateHubIdentity() (string, string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	encrypted, err := crypto.EncryptSecret("hub-binding-test", base64.StdEncoding.EncodeToString(private))
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(public), encrypted, nil
}
