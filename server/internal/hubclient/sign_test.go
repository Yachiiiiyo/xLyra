package hubclient

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"xlyra/server/internal/store"
)

func TestSharedSignatureVector(t *testing.T) {
	seed, err := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed: %v len=%d", err, len(seed))
	}
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	if got := base64.StdEncoding.EncodeToString(public); got != "ebVWLo/mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ=" {
		t.Fatalf("public key = %s", got)
	}
	body := []byte(`{"xlyraVersion":"1.15.1"}`)
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != "9a2cfd7d0bd8e42ebbaacdb465e429171a3a036d08489168ed3ac91f140f205b" {
		t.Fatalf("body hash = %s", got)
	}
	message := CanonicalMessage(httpMethodPost, "/api/instance/heartbeat", 1700000000, body)
	const wantMessage = "xlyra-instance-v1\nPOST\n/api/instance/heartbeat\n1700000000\n9a2cfd7d0bd8e42ebbaacdb465e429171a3a036d08489168ed3ac91f140f205b"
	if message != wantMessage {
		t.Fatalf("message = %q", message)
	}
	if got := Sign(private, message); got != "wr8msIKGl7GJuhcWZ0H3oxkSLFYzSBcNJ8ktAEwSSnJt6TzSPeHDI9lqe/N2LOcK0L4oVaS4SvTBDF0/r5HICg==" {
		t.Fatalf("signature = %s", got)
	}
}

const httpMethodPost = "POST"

func TestPollTransitionsAndBackoff(t *testing.T) {
	next, last, done := applyPollStatus("pending")
	if next != store.HubStatusPairing || last != "" || done {
		t.Fatalf("pending -> %s %q %v", next, last, done)
	}
	next, last, done = applyPollStatus("approved")
	if next != store.HubStatusBound || !done {
		t.Fatalf("approved -> %s %v", next, done)
	}
	next, last, done = applyPollStatus("denied")
	if next != store.HubStatusUnbound || last != "配对被拒绝" || !done {
		t.Fatalf("denied -> %s %q %v", next, last, done)
	}
	next, last, done = applyPollStatus("expired")
	if next != store.HubStatusUnbound || last != "配对已过期" || !done {
		t.Fatalf("expired -> %s %q %v", next, last, done)
	}
	if got := nextBackoff(0, 30*time.Second, time.Hour); got != 30*time.Second {
		t.Fatalf("backoff start = %s", got)
	}
	if got := nextBackoff(30*time.Second, 30*time.Second, time.Hour); got != time.Minute {
		t.Fatalf("backoff step = %s", got)
	}
	if got := nextBackoff(40*time.Minute, 30*time.Second, time.Hour); got != time.Hour {
		t.Fatalf("backoff cap = %s", got)
	}
	if got := nextBackoff(time.Hour, 30*time.Second, time.Hour); got != time.Hour {
		t.Fatalf("backoff stay = %s", got)
	}
}
