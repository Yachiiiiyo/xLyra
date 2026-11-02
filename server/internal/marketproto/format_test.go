package marketproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPAE(t *testing.T) {
	pt := "application/vnd.xlyra.root+json"
	if len(pt) != 31 {
		t.Fatalf("payloadType length %d", len(pt))
	}
	got := string(PAE(pt, []byte(`{"v":1}`)))
	want := "DSSEv1 31 application/vnd.xlyra.root+json 7 {\"v\":1}"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if string(PAE("ab", nil)) != "DSSEv1 2 ab 0 " {
		t.Fatalf("empty payload got %q", PAE("ab", nil))
	}
}

func TestParseVersion(t *testing.T) {
	v, err := ParseVersion("0.0.0")
	if err != nil || v != (Version{}) {
		t.Fatalf("zero: %v %v", v, err)
	}
	v, err = ParseVersion("1.2.3")
	if err != nil || v != (Version{1, 2, 3}) {
		t.Fatalf("parse: %v %v", v, err)
	}
	if Compare(Version{1, 0, 0}, Version{1, 0, 1}) != -1 {
		t.Fatal("less")
	}
	if Compare(Version{2, 0, 0}, Version{1, 9, 9}) != 1 {
		t.Fatal("greater")
	}
	if Compare(Version{1, 4, 2}, Version{1, 4, 2}) != 0 {
		t.Fatal("equal")
	}
	for _, s := range []string{"1.0", "1.0.0.1", "1.02.3", "01.2.3", "1.0.0-rc.1", "1.0.0+build", "", "v1.0.0"} {
		if _, err := ParseVersion(s); Code(err) != CodeBadVersion {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestKeyID(t *testing.T) {
	raw, _ := hex.DecodeString(strings.Repeat("11", 32))
	priv := ed25519.NewKeyFromSeed(raw)
	pub := priv.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	want := "SHA256:" + base64.StdEncoding.EncodeToString(sum[:])
	if KeyID(pub) != want {
		t.Fatalf("got %s", KeyID(pub))
	}
	parsed, err := ParsePublicKey(base64.StdEncoding.EncodeToString(pub))
	if err != nil || !bytes.Equal(parsed, pub) {
		t.Fatalf("parse: %v", err)
	}
	if _, err := ParsePublicKey(base64.StdEncoding.EncodeToString(make([]byte, 31))); Code(err) != CodeBadField {
		t.Fatalf("short: %v", err)
	}
}

func TestCode(t *testing.T) {
	if Code(nil) != "" || Code(errors.New("x")) != "" {
		t.Fatal("non-protocol")
	}
	if Code(&Error{Code: CodeBadField, Msg: "x"}) != CodeBadField {
		t.Fatal("code")
	}
}

func TestSizeLimit(t *testing.T) {
	now := time.Unix(0, 0)
	_, err := VerifyRoot(bytes.Repeat([]byte{'x'}, MaxRootBytes), nil, State{}, now)
	if Code(err) != CodeBadEnvelope {
		t.Fatalf("root boundary: %v", err)
	}
	_, err = VerifyRoot(bytes.Repeat([]byte{'x'}, MaxRootBytes+1), nil, State{}, now)
	if Code(err) != CodeBadField {
		t.Fatalf("root over: %v", err)
	}
	_, err = VerifyIndex(bytes.Repeat([]byte{'x'}, MaxIndexBytes+1), &Root{}, State{}, now)
	if Code(err) != CodeBadField {
		t.Fatalf("index over: %v", err)
	}
	_, err = VerifyRevocations(bytes.Repeat([]byte{'x'}, MaxRevocationsBytes+1), &Root{}, State{}, now)
	if Code(err) != CodeBadField {
		t.Fatalf("revocations over: %v", err)
	}
}

func TestEndpointsAndLists(t *testing.T) {
	hub := "https://hub.example"
	if err := validateEndpoints(Endpoints{Registry: []string{"https://registry.example/v1/"}, Cloud: &hub}); err != nil {
		t.Fatal(err)
	}
	if Code(validateEndpoints(Endpoints{Registry: []string{"http://registry.example/v1/"}})) != CodeBadField {
		t.Fatal("http")
	}
	if Code(validateEndpoints(Endpoints{Registry: []string{"https://registry.example/v1"}})) != CodeBadField {
		t.Fatal("slash")
	}
	bad := "http://hub.example"
	if Code(validateEndpoints(Endpoints{Registry: []string{"https://registry.example/v1/"}, Cloud: &bad})) != CodeBadField {
		t.Fatal("hub")
	}
	if err := requireSorted("hosts", []string{}); err != nil {
		t.Fatal(err)
	}
	if err := requireSorted("hosts", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if Code(requireSorted("hosts", nil)) != CodeBadField {
		t.Fatal("nil")
	}
	if Code(requireSorted("hosts", []string{"b", "a"})) != CodeBadField {
		t.Fatal("order")
	}
	if Code(requireSorted("hosts", []string{"a", "a"})) != CodeBadField {
		t.Fatal("dup")
	}
	if err := maxRunes("name", strings.Repeat("名", 64), 64); err != nil {
		t.Fatal(err)
	}
	if Code(maxRunes("name", strings.Repeat("名", 65), 64)) != CodeBadField {
		t.Fatal("runes")
	}
}
