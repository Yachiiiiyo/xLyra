package marketproto_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocol "xlyra/server/internal/marketproto"
)

func TestVectors(t *testing.T) {
	dir := filepath.Join("testdata", "vectors")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	n := 0
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var vecs []vectorDoc
		if err := json.Unmarshal(data, &vecs); err != nil {
			t.Fatalf("%s: %v", ent.Name(), err)
		}
		fileCat := strings.TrimSuffix(ent.Name(), ".json")
		for _, vec := range vecs {
			n++
			vec := vec
			t.Run(vec.Name, func(t *testing.T) {
				if category(vec.Name) != fileCat {
					t.Fatalf("name %s in %s", vec.Name, ent.Name())
				}
				ok, code, effects, cmp := evalVector(t, vec)
				if ok != vec.Expect.OK || code != vec.Expect.Code || cmp != vec.Expect.Compare || !sameEffects(effects, vec.Expect.Effects) {
					t.Fatalf("got ok=%v code=%q compare=%d effects=%+v", ok, code, cmp, effects)
				}
			})
		}
	}
	if n == 0 {
		t.Fatal("no vectors")
	}
}

type vectorDoc struct {
	Name     string            `json:"name"`
	Now      string            `json:"now"`
	Keys     map[string]keyDoc `json:"keys"`
	RootKeys []string          `json:"rootKeys"`
	State    *stateDoc         `json:"state"`
	Inputs   inputDoc          `json:"inputs"`
	Expect   expectDoc         `json:"expect"`
}

type keyDoc struct {
	Seed      string `json:"seed"`
	PublicKey string `json:"publicKey"`
	KeyID     string `json:"keyId"`
}

type stateDoc struct {
	Root        *seqDoc `json:"root"`
	Index       *seqDoc `json:"index"`
	Revocations *seqDoc `json:"revocations"`
}

type seqDoc struct {
	Seq           int64  `json:"seq"`
	PayloadSHA256 string `json:"payloadSha256"`
}

type inputDoc struct {
	Root          string                 `json:"root"`
	Index         string                 `json:"index"`
	Revocations   string                 `json:"revocations"`
	Attestation   string                 `json:"attestation"`
	KeyTransition string                 `json:"keyTransition"`
	Package       *protocol.PackageFacts `json:"package"`
	Fingerprints  []string               `json:"fingerprints"`
	File          string                 `json:"file"`
	FileSHA256    string                 `json:"fileSha256"`
	Size          int64                  `json:"size"`
	Target        *protocol.Target       `json:"target"`
	Version       *struct {
		A string `json:"a"`
		B string `json:"b"`
	} `json:"version"`
}

type expectDoc struct {
	OK      bool     `json:"ok"`
	Code    string   `json:"code"`
	Effects []effect `json:"effects"`
	Compare int      `json:"compare"`
}

type effect struct {
	EntryID    string `json:"entryId"`
	Action     string `json:"action"`
	RollbackTo string `json:"rollbackTo"`
}

func category(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

func evalVector(t *testing.T, v vectorDoc) (bool, string, []protocol.Effect, int) {
	t.Helper()
	switch category(v.Name) {
	case "envelope":
		ok, code := evalEnvelope(t, v)
		return ok, code, nil, 0
	case "root":
		ok, code := evalRoot(t, v)
		return ok, code, nil, 0
	case "index":
		ok, code := evalIndex(t, v)
		return ok, code, nil, 0
	case "revocations":
		ok, code := evalRevocations(t, v)
		return ok, code, nil, 0
	case "attestation":
		ok, code := evalAttestation(t, v)
		return ok, code, nil, 0
	case "version":
		return evalVersion(v)
	case "revocation_match":
		return evalMatch(t, v)
	case "key_transition":
		ok, code := evalTransition(t, v)
		return ok, code, nil, 0
	default:
		t.Fatalf("unknown category %s", v.Name)
		return false, "", nil, 0
	}
}

func evalEnvelope(t *testing.T, v vectorDoc) (bool, string) {
	t.Helper()
	now := mustNow(t, v.Now)
	pubs := mustPubs(t, v.RootKeys)
	if v.Inputs.Attestation != "" {
		root, err := protocol.VerifyRoot([]byte(v.Inputs.Root), pubs, docState(v.State, "root"), now)
		if err != nil {
			t.Fatalf("setup root: %v", err)
		}
		_, err = protocol.VerifyAttestationRaw([]byte(v.Inputs.Attestation), root, now)
		if err != nil {
			return false, protocol.Code(err)
		}
		return true, ""
	}
	_, err := protocol.VerifyRoot([]byte(v.Inputs.Root), pubs, docState(v.State, "root"), now)
	if err != nil {
		return false, protocol.Code(err)
	}
	return true, ""
}

func evalRoot(t *testing.T, v vectorDoc) (bool, string) {
	t.Helper()
	_, err := protocol.VerifyRoot([]byte(v.Inputs.Root), mustPubs(t, v.RootKeys), docState(v.State, "root"), mustNow(t, v.Now))
	if err != nil {
		return false, protocol.Code(err)
	}
	return true, ""
}

func evalIndex(t *testing.T, v vectorDoc) (bool, string) {
	t.Helper()
	now := mustNow(t, v.Now)
	root, err := protocol.VerifyRoot([]byte(v.Inputs.Root), mustPubs(t, v.RootKeys), docState(v.State, "root"), now)
	if err != nil {
		t.Fatalf("setup root: %v", err)
	}
	_, err = protocol.VerifyIndex([]byte(v.Inputs.Index), root, docState(v.State, "index"), now)
	if err != nil {
		return false, protocol.Code(err)
	}
	return true, ""
}

func evalRevocations(t *testing.T, v vectorDoc) (bool, string) {
	t.Helper()
	now := mustNow(t, v.Now)
	root, err := protocol.VerifyRoot([]byte(v.Inputs.Root), mustPubs(t, v.RootKeys), docState(v.State, "root"), now)
	if err != nil {
		t.Fatalf("setup root: %v", err)
	}
	_, err = protocol.VerifyRevocations([]byte(v.Inputs.Revocations), root, docState(v.State, "revocations"), now)
	if err != nil {
		return false, protocol.Code(err)
	}
	return true, ""
}

func evalAttestation(t *testing.T, v vectorDoc) (bool, string) {
	t.Helper()
	now := mustNow(t, v.Now)
	root, err := protocol.VerifyRoot([]byte(v.Inputs.Root), mustPubs(t, v.RootKeys), docState(v.State, "root"), now)
	if err != nil {
		t.Fatalf("setup root: %v", err)
	}
	var att *protocol.Attestation
	if v.Inputs.Attestation != "" {
		att, err = protocol.VerifyAttestationRaw([]byte(v.Inputs.Attestation), root, now)
		if err != nil {
			return false, protocol.Code(err)
		}
	}
	if v.Inputs.Package != nil {
		if err := protocol.CheckPackage(att, *v.Inputs.Package, v.Inputs.Fingerprints); err != nil {
			return false, protocol.Code(err)
		}
	}
	if v.Inputs.File != "" {
		raw, err := base64.StdEncoding.DecodeString(v.Inputs.File)
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		if err := protocol.CheckFile(raw, v.Inputs.FileSHA256, v.Inputs.Size); err != nil {
			return false, protocol.Code(err)
		}
	}
	return true, ""
}

func evalVersion(v vectorDoc) (bool, string, []protocol.Effect, int) {
	if v.Inputs.Version == nil {
		return false, protocol.CodeBadField, nil, 0
	}
	a, errA := protocol.ParseVersion(v.Inputs.Version.A)
	if v.Inputs.Version.B == "" {
		if errA != nil {
			return false, protocol.Code(errA), nil, 0
		}
		return true, "", nil, 0
	}
	b, errB := protocol.ParseVersion(v.Inputs.Version.B)
	if errA != nil {
		return false, protocol.Code(errA), nil, 0
	}
	if errB != nil {
		return false, protocol.Code(errB), nil, 0
	}
	return true, "", nil, protocol.Compare(a, b)
}

func evalMatch(t *testing.T, v vectorDoc) (bool, string, []protocol.Effect, int) {
	t.Helper()
	now := mustNow(t, v.Now)
	root, err := protocol.VerifyRoot([]byte(v.Inputs.Root), mustPubs(t, v.RootKeys), docState(v.State, "root"), now)
	if err != nil {
		t.Fatalf("setup root: %v", err)
	}
	rev, err := protocol.VerifyRevocations([]byte(v.Inputs.Revocations), root, docState(v.State, "revocations"), now)
	if err != nil {
		t.Fatalf("setup revocations: %v", err)
	}
	if v.Inputs.Target == nil {
		t.Fatal("missing target")
	}
	return true, "", protocol.MatchRevocations(rev, *v.Inputs.Target), 0
}

func evalTransition(t *testing.T, v vectorDoc) (bool, string) {
	t.Helper()
	if len(v.RootKeys) != 1 {
		t.Fatal("key transition rootKeys")
	}
	pub, err := protocol.ParsePublicKey(v.RootKeys[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = protocol.VerifyKeyTransition([]byte(v.Inputs.KeyTransition), pub)
	if err != nil {
		return false, protocol.Code(err)
	}
	return true, ""
}

func docState(st *stateDoc, which string) protocol.State {
	if st == nil {
		return protocol.State{}
	}
	var s *seqDoc
	switch which {
	case "root":
		s = st.Root
	case "index":
		s = st.Index
	case "revocations":
		s = st.Revocations
	}
	if s == nil {
		return protocol.State{}
	}
	return protocol.State{Seq: s.Seq, PayloadSHA256: s.PayloadSHA256}
}

func mustNow(t *testing.T, s string) time.Time {
	t.Helper()
	if s == "" {
		return time.Time{}
	}
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func sameEffects(got []protocol.Effect, want []effect) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].EntryID != want[i].EntryID || got[i].Action != want[i].Action || got[i].RollbackTo != want[i].RollbackTo {
			return false
		}
	}
	return true
}

func mustPubs(t *testing.T, keys []string) []ed25519.PublicKey {
	t.Helper()
	out := make([]ed25519.PublicKey, 0, len(keys))
	for _, k := range keys {
		pub, err := protocol.ParsePublicKey(k)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pub)
	}
	return out
}
