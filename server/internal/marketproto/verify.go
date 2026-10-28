package marketproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// ClockSkew is the allowed difference between the verifier clock and
	// notBefore / notAfter / expiresAt.
	ClockSkew = 10 * time.Minute

	// MaxRootBytes, MaxIndexBytes, and MaxRevocationsBytes are file-size caps
	// for the signed envelope bytes.
	MaxRootBytes        = 64 * 1024
	MaxIndexBytes       = 32 * 1024 * 1024
	MaxRevocationsBytes = 4 * 1024 * 1024
)

// State is the last accepted seq and the sha256 of that payload's raw bytes.
// The zero value means the caller has not accepted a document yet.
type State struct {
	Seq           int64  `json:"seq"`
	PayloadSHA256 string `json:"payloadSha256"`
}

// PackageFacts is the manifest view a caller extracted from an unpacked plugin.
type PackageFacts struct {
	PackageDigest     string   `json:"packageDigest"`
	SignerFingerprint string   `json:"signerFingerprint"`
	PluginID          string   `json:"pluginId"`
	Version           string   `json:"version"`
	Kind              string   `json:"kind"`
	Hosts             []string `json:"hosts"`
	Permissions       []string `json:"permissions"`
}

// VerifyRoot checks a root.json envelope against the built-in root public keys.
func VerifyRoot(raw []byte, rootKeys []ed25519.PublicKey, st State, now time.Time) (*Root, error) {
	if len(raw) > MaxRootBytes {
		return nil, errCode(CodeBadField, "root exceeds size limit")
	}
	env, payload, err := parseEnvelope(raw, PayloadTypeRoot)
	if err != nil {
		return nil, err
	}
	if err := evalSigs(env.PayloadType, payload, env.Signatures, func(id string) (ed25519.PublicKey, sigClass) {
		return lookupRoot(id, rootKeys)
	}, nil); err != nil {
		return nil, err
	}
	if err := checkV(payload); err != nil {
		return nil, err
	}
	seq, err := payloadSeq(payload)
	if err != nil {
		return nil, err
	}
	if err := checkSeq(seq, payload, st); err != nil {
		return nil, err
	}
	return validateRoot(payload)
}

// VerifyIndex checks an index.json envelope using online keys that have the
// index role. Embedded attestations are checked for envelope shape only.
func VerifyIndex(raw []byte, root *Root, st State, now time.Time) (*Index, error) {
	if len(raw) > MaxIndexBytes {
		return nil, errCode(CodeBadField, "index exceeds size limit")
	}
	if root == nil {
		return nil, errCode(CodeBadField, "missing root")
	}
	env, payload, err := parseEnvelope(raw, PayloadTypeIndex)
	if err != nil {
		return nil, err
	}
	if err := evalSigs(env.PayloadType, payload, env.Signatures, func(id string) (ed25519.PublicKey, sigClass) {
		return lookupDocumentKey(id, RoleIndex, root, now)
	}, nil); err != nil {
		return nil, err
	}
	if err := checkV(payload); err != nil {
		return nil, err
	}
	seq, err := payloadSeq(payload)
	if err != nil {
		return nil, err
	}
	if err := checkSeq(seq, payload, st); err != nil {
		return nil, err
	}
	return validateIndex(payload, now)
}

// VerifyRevocations checks a revocations.json envelope using online keys that
// have the revocations role.
func VerifyRevocations(raw []byte, root *Root, st State, now time.Time) (*Revocations, error) {
	if len(raw) > MaxRevocationsBytes {
		return nil, errCode(CodeBadField, "revocations exceeds size limit")
	}
	if root == nil {
		return nil, errCode(CodeBadField, "missing root")
	}
	env, payload, err := parseEnvelope(raw, PayloadTypeRevocations)
	if err != nil {
		return nil, err
	}
	if err := evalSigs(env.PayloadType, payload, env.Signatures, func(id string) (ed25519.PublicKey, sigClass) {
		return lookupDocumentKey(id, RoleRevocations, root, now)
	}, nil); err != nil {
		return nil, err
	}
	if err := checkV(payload); err != nil {
		return nil, err
	}
	seq, err := payloadSeq(payload)
	if err != nil {
		return nil, err
	}
	if err := checkSeq(seq, payload, st); err != nil {
		return nil, err
	}
	return validateRevocations(payload, now)
}

// VerifyAttestation checks an attestation envelope.
// Online keys need the attestation role and must be inside their skew window.
// A retired key is accepted only when issuedAt lies in [notBefore, notAfter]
// with no clock skew (issuedAt is the signer's timestamp, not the local clock).
func VerifyAttestation(env Envelope, root *Root, now time.Time) (*Attestation, error) {
	if root == nil {
		return nil, errCode(CodeBadField, "missing root")
	}
	if env.PayloadType != PayloadTypeAttestation {
		return nil, errCode(CodeBadPayloadType, "unexpected payload type")
	}
	if env.Payload == "" {
		return nil, errCode(CodeBadEnvelope, "missing payload")
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return nil, errCode(CodeBadEnvelope, "payload is not standard base64")
	}
	if err := evalSigs(env.PayloadType, payload, env.Signatures, func(id string) (ed25519.PublicKey, sigClass) {
		return lookupAttestationKey(id, root, now)
	}, func(keyID string) (bool, error) {
		return retiredCovers(keyID, root, payload)
	}); err != nil {
		return nil, err
	}
	if err := checkV(payload); err != nil {
		return nil, err
	}
	return validateAttestation(payload)
}

// VerifyAttestationRaw unmarshals an attestation envelope and verifies it.
func VerifyAttestationRaw(raw []byte, root *Root, now time.Time) (*Attestation, error) {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || trim[0] != '{' {
		return nil, errCode(CodeBadEnvelope, "envelope is not a json object")
	}
	var env Envelope
	if err := json.Unmarshal(trim, &env); err != nil {
		return nil, errCode(CodeBadEnvelope, "invalid envelope json")
	}
	return VerifyAttestation(env, root, now)
}

// CheckPackage compares an attestation with the unpacked package.
// Mismatches are reported in a fixed order: digest, developer, manifest.
func CheckPackage(att *Attestation, pkg PackageFacts, fingerprints []string) error {
	if att == nil {
		return errCode(CodeBadField, "missing attestation")
	}
	if pkg.PackageDigest != att.PackageDigest {
		return errCode(CodeDigestMismatch, "package digest mismatch")
	}
	if pkg.SignerFingerprint != att.DeveloperFingerprint || !containsStr(fingerprints, pkg.SignerFingerprint) {
		return errCode(CodeDeveloperMismatch, "developer fingerprint mismatch")
	}
	if pkg.PluginID != att.PluginID || pkg.Version != att.Version || pkg.Kind != att.Kind ||
		!sameSet(pkg.Hosts, att.Hosts) || !sameSet(pkg.Permissions, att.Permissions) {
		return errCode(CodeManifestMismatch, "manifest does not match attestation")
	}
	return nil
}

// FileSHA256 is the lowercase hex sha256 of raw.
func FileSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CheckFile reports file_hash_mismatch when the bytes differ in sha256 or length.
func CheckFile(raw []byte, wantSHA string, wantSize int64) error {
	if int64(len(raw)) != wantSize || FileSHA256(raw) != wantSHA {
		return errCode(CodeFileHashMismatch, "file hash or size mismatch")
	}
	return nil
}

type sigClass int

const (
	sigIgnore sigClass = iota
	sigAccept
	sigExpired
	sigWrongRole
	sigRetired
)

func parseEnvelope(raw []byte, wantType string) (Envelope, []byte, error) {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || trim[0] != '{' {
		return Envelope{}, nil, errCode(CodeBadEnvelope, "envelope is not a json object")
	}
	var env Envelope
	if err := json.Unmarshal(trim, &env); err != nil {
		return Envelope{}, nil, errCode(CodeBadEnvelope, "invalid envelope json")
	}
	if env.PayloadType != wantType {
		return Envelope{}, nil, errCode(CodeBadPayloadType, "unexpected payload type")
	}
	if env.Payload == "" {
		return Envelope{}, nil, errCode(CodeBadEnvelope, "missing payload")
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		return Envelope{}, nil, errCode(CodeBadEnvelope, "payload is not standard base64")
	}
	return env, payload, nil
}

// evalSigs accepts the envelope when any recognized signature is valid.
// A bad signature on a currently-acceptable key is bad_signature only when no
// other signature is acceptable. key_expired and key_role require a valid
// signature from that key. Unrecognized key ids are ignored.
func evalSigs(payloadType string, payload []byte, sigs []Signature, lookup func(string) (ed25519.PublicKey, sigClass), retiredWindow func(string) (bool, error)) error {
	pae := PAE(payloadType, payload)
	var sawBad, sawExpired, sawRole bool
	var retiredErr error
	for _, s := range sigs {
		pub, class := lookup(s.KeyID)
		if class == sigIgnore || pub == nil {
			continue
		}
		ok := signatureOK(pub, pae, s.Sig)
		switch class {
		case sigAccept:
			if ok {
				return nil
			}
			sawBad = true
		case sigExpired:
			if ok {
				sawExpired = true
			}
		case sigWrongRole:
			if ok {
				sawRole = true
			}
		case sigRetired:
			if !ok || retiredWindow == nil {
				continue
			}
			in, err := retiredWindow(s.KeyID)
			if err != nil {
				retiredErr = err
				continue
			}
			if in {
				return nil
			}
			sawExpired = true
		}
	}
	if sawBad {
		return errCode(CodeBadSignature, "signature invalid")
	}
	if sawExpired {
		return errCode(CodeKeyExpired, "signing key is outside its validity window")
	}
	if sawRole {
		return errCode(CodeKeyRole, "signing key lacks the required role")
	}
	if retiredErr != nil {
		return retiredErr
	}
	return errCode(CodeUnknownKey, "no recognized signing key")
}

func signatureOK(pub ed25519.PublicKey, pae []byte, sigB64 string) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, pae, raw)
}

func lookupRoot(keyID string, keys []ed25519.PublicKey) (ed25519.PublicKey, sigClass) {
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && KeyID(k) == keyID {
			return k, sigAccept
		}
	}
	return nil, sigIgnore
}

func lookupDocumentKey(keyID, role string, root *Root, now time.Time) (ed25519.PublicKey, sigClass) {
	if pub, class, found := classifyOnline(keyID, role, root.OnlineKeys, now); found {
		return pub, class
	}
	// Retired keys cannot sign an index or a revocation list.
	return nil, sigIgnore
}

func lookupAttestationKey(keyID string, root *Root, now time.Time) (ed25519.PublicKey, sigClass) {
	if pub, class, found := classifyOnline(keyID, RoleAttestation, root.OnlineKeys, now); found {
		return pub, class
	}
	for _, k := range root.RetiredKeys {
		if k.KeyID != keyID {
			continue
		}
		pub, err := ParsePublicKey(k.PublicKey)
		if err != nil || KeyID(pub) != k.KeyID {
			return nil, sigIgnore
		}
		return pub, sigRetired
	}
	return nil, sigIgnore
}

func classifyOnline(keyID, role string, keys []OnlineKey, now time.Time) (ed25519.PublicKey, sigClass, bool) {
	for _, k := range keys {
		if k.KeyID != keyID {
			continue
		}
		pub, err := ParsePublicKey(k.PublicKey)
		if err != nil || KeyID(pub) != k.KeyID {
			return nil, sigIgnore, true
		}
		active, ok := withinSkew(k.NotBefore, k.NotAfter, now)
		if !ok {
			return nil, sigIgnore, true
		}
		if !hasRole(k.Roles, role) {
			return pub, sigWrongRole, true
		}
		if !active {
			return pub, sigExpired, true
		}
		return pub, sigAccept, true
	}
	return nil, sigIgnore, false
}

func retiredCovers(keyID string, root *Root, payload []byte) (bool, error) {
	issued, err := peekIssuedAt(payload)
	if err != nil {
		return false, err
	}
	for _, k := range root.RetiredKeys {
		if k.KeyID != keyID {
			continue
		}
		return issuedInWindow(k.NotBefore, k.NotAfter, issued)
	}
	return false, errCode(CodeUnknownKey, "retired key missing")
}

func peekIssuedAt(payload []byte) (time.Time, error) {
	var probe struct {
		IssuedAt string `json:"issuedAt"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return time.Time{}, errCode(CodeBadField, "issuedAt")
	}
	return parseTime(probe.IssuedAt)
}

func issuedInWindow(notBefore, notAfter string, issued time.Time) (bool, error) {
	begin, err := parseTime(notBefore)
	if err != nil {
		return false, err
	}
	end, err := parseTime(notAfter)
	if err != nil {
		return false, err
	}
	if issued.Before(begin) || issued.After(end) {
		return false, nil
	}
	return true, nil
}

func withinSkew(notBefore, notAfter string, now time.Time) (active bool, ok bool) {
	begin, err1 := time.Parse(time.RFC3339, notBefore)
	end, err2 := time.Parse(time.RFC3339, notAfter)
	if err1 != nil || err2 != nil {
		return false, false
	}
	if now.Before(begin.Add(-ClockSkew)) || now.After(end.Add(ClockSkew)) {
		return false, true
	}
	return true, true
}

func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func checkSeq(seq int64, payload []byte, st State) error {
	if st.Seq == 0 && st.PayloadSHA256 == "" {
		return nil
	}
	if seq < st.Seq {
		return errCode(CodeSeqRollback, "sequence number is lower than the last accepted one")
	}
	if seq == st.Seq && FileSHA256(payload) != st.PayloadSHA256 {
		return errCode(CodeSeqConflict, "sequence number was reused with different content")
	}
	return nil
}

func checkV(payload []byte) error {
	m, err := jsonObject(payload)
	if err != nil {
		return err
	}
	raw, ok := m["v"]
	if !ok {
		return errCode(CodeBadField, "missing v")
	}
	n, err := jsonInt(raw)
	if err != nil {
		return errCode(CodeBadField, "v is not an integer")
	}
	if n > 1 {
		return errCode(CodeUnsupportedVersion, "unsupported payload version")
	}
	if n != 1 {
		return errCode(CodeBadField, "invalid payload version")
	}
	return nil
}

func payloadSeq(payload []byte) (int64, error) {
	m, err := jsonObject(payload)
	if err != nil {
		return 0, err
	}
	raw, ok := m["seq"]
	if !ok {
		return 0, errCode(CodeBadField, "missing seq")
	}
	n, err := jsonInt(raw)
	if err != nil || n < 0 {
		return 0, errCode(CodeBadField, "seq")
	}
	return n, nil
}

func jsonObject(raw []byte) (map[string]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, errCode(CodeBadField, "expected json object")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errCode(CodeBadField, "expected json object")
	}
	return m, nil
}

func jsonInt(raw json.RawMessage) (int64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, strconv.ErrSyntax
	}
	switch raw[0] {
	case '{', '[', '"', 't', 'f', 'n':
		return 0, strconv.ErrSyntax
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var n json.Number
	if err := dec.Decode(&n); err != nil {
		return 0, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return 0, strconv.ErrSyntax
	}
	s := n.String()
	if strings.ContainsAny(s, ".eE+") {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseInt(s, 10, 64)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errCode(CodeBadField, "invalid time")
	}
	return t, nil
}

func documentTimes(generatedAt, expiresAt string) (time.Time, time.Time, error) {
	gen, err := parseTime(generatedAt)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	exp, err := parseTime(expiresAt)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if gen.After(exp) {
		return time.Time{}, time.Time{}, errCode(CodeBadField, "generatedAt is after expiresAt")
	}
	return gen, exp, nil
}

func pastExpiry(exp, now time.Time) bool {
	return now.After(exp.Add(ClockSkew))
}

func validateRoot(payload []byte) (*Root, error) {
	var root Root
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, errCode(CodeBadField, "invalid json")
	}
	if _, err := parseTime(root.IssuedAt); err != nil {
		return nil, err
	}
	if len(root.OnlineKeys) == 0 {
		return nil, errCode(CodeBadField, "onlineKeys")
	}
	for _, k := range root.OnlineKeys {
		if err := validateOnlineKey(k); err != nil {
			return nil, err
		}
	}
	for _, k := range root.RetiredKeys {
		if err := validateRetiredKey(k); err != nil {
			return nil, err
		}
	}
	if err := validateEndpoints(root.Endpoints); err != nil {
		return nil, err
	}
	return &root, nil
}

func validateOnlineKey(k OnlineKey) error {
	pub, err := ParsePublicKey(k.PublicKey)
	if err != nil {
		return err
	}
	if k.KeyID == "" || KeyID(pub) != k.KeyID {
		return errCode(CodeBadField, "keyId")
	}
	if len(k.Roles) == 0 {
		return errCode(CodeBadField, "roles")
	}
	for _, role := range k.Roles {
		switch role {
		case RoleIndex, RoleRevocations, RoleAttestation:
		default:
			return errCode(CodeBadField, "roles")
		}
	}
	return validateWindow(k.NotBefore, k.NotAfter)
}

func validateRetiredKey(k RetiredKey) error {
	pub, err := ParsePublicKey(k.PublicKey)
	if err != nil {
		return err
	}
	if k.KeyID == "" || KeyID(pub) != k.KeyID {
		return errCode(CodeBadField, "keyId")
	}
	return validateWindow(k.NotBefore, k.NotAfter)
}

func validateWindow(notBefore, notAfter string) error {
	begin, err := parseTime(notBefore)
	if err != nil {
		return err
	}
	end, err := parseTime(notAfter)
	if err != nil {
		return err
	}
	if !begin.Before(end) {
		return errCode(CodeBadField, "notBefore must be before notAfter")
	}
	return nil
}

func validateEndpoints(ep Endpoints) error {
	if len(ep.Registry) == 0 {
		return errCode(CodeBadField, "registry")
	}
	for _, u := range ep.Registry {
		if !strings.HasPrefix(u, "https://") || !strings.HasSuffix(u, "/") {
			return errCode(CodeBadField, "registry")
		}
	}
	if ep.Hub != nil && !strings.HasPrefix(*ep.Hub, "https://") {
		return errCode(CodeBadField, "hub")
	}
	if ep.Push != nil && !strings.HasPrefix(*ep.Push, "https://") {
		return errCode(CodeBadField, "push")
	}
	return nil
}

func validateIndex(payload []byte, now time.Time) (*Index, error) {
	var idx Index
	if err := json.Unmarshal(payload, &idx); err != nil {
		return nil, errCode(CodeBadField, "invalid json")
	}
	_, exp, err := documentTimes(idx.GeneratedAt, idx.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if idx.Plugins == nil {
		return nil, errCode(CodeBadField, "plugins")
	}
	seen := map[string]struct{}{}
	for _, p := range idx.Plugins {
		if err := validatePlugin(p, seen); err != nil {
			return nil, err
		}
	}
	if pastExpiry(exp, now) {
		return nil, errCode(CodeExpired, "index has expired")
	}
	return &idx, nil
}

func validatePlugin(p Plugin, seen map[string]struct{}) error {
	if p.ID == "" {
		return errCode(CodeBadField, "plugin id")
	}
	if _, ok := seen[p.ID]; ok {
		return errCode(CodeBadField, "duplicate plugin id")
	}
	seen[p.ID] = struct{}{}
	if p.Developer.Namespace == "" || !strings.HasPrefix(p.ID, p.Developer.Namespace+".") {
		return errCode(CodeBadField, "plugin id")
	}
	if len(p.Developer.Fingerprints) == 0 {
		return errCode(CodeBadField, "fingerprints")
	}
	for _, fp := range p.Developer.Fingerprints {
		if fp == "" {
			return errCode(CodeBadField, "fingerprints")
		}
	}
	if err := maxRunes("name", p.Name, 64); err != nil {
		return err
	}
	if err := maxRunes("summary", p.Summary, 200); err != nil {
		return err
	}
	if p.Icon != nil {
		sum := p.Icon.SHA256
		if !isSHA256Hex(sum) || p.Icon.Path != "icons/"+sum+".png" {
			return errCode(CodeBadField, "icon")
		}
	}
	if len(p.Versions) == 0 {
		return errCode(CodeBadField, "versions")
	}
	var prev Version
	for i, ver := range p.Versions {
		cur, err := ParseVersion(ver.Version)
		if err != nil {
			return err
		}
		if i > 0 && Compare(prev, cur) <= 0 {
			return errCode(CodeBadField, "versions must be strictly descending")
		}
		prev = cur
		if _, err := parseTime(ver.PublishedAt); err != nil {
			return err
		}
		if !isSHA256Hex(ver.FileSHA256) {
			return errCode(CodeBadField, "fileSha256")
		}
		if !isSHA256Hex(ver.PackageDigest) {
			return errCode(CodeBadField, "packageDigest")
		}
		if ver.Size <= 0 {
			return errCode(CodeBadField, "size")
		}
		if err := requireSorted("hosts", ver.Hosts); err != nil {
			return err
		}
		if err := requireSorted("permissions", ver.Permissions); err != nil {
			return err
		}
		if err := maxRunes("changelog", ver.Changelog, 4000); err != nil {
			return err
		}
		if err := validEmbeddedAttestation(ver.Attestation); err != nil {
			return err
		}
	}
	return nil
}

func validEmbeddedAttestation(env Envelope) error {
	if env.PayloadType != PayloadTypeAttestation {
		return errCode(CodeBadField, "attestation envelope")
	}
	if env.Payload == "" {
		return errCode(CodeBadField, "attestation payload")
	}
	if _, err := base64.StdEncoding.DecodeString(env.Payload); err != nil {
		return errCode(CodeBadField, "attestation payload")
	}
	if len(env.Signatures) == 0 {
		return errCode(CodeBadField, "attestation signatures")
	}
	for _, s := range env.Signatures {
		if s.KeyID == "" || s.Sig == "" {
			return errCode(CodeBadField, "attestation signature")
		}
		if _, err := base64.StdEncoding.DecodeString(s.Sig); err != nil {
			return errCode(CodeBadField, "attestation signature")
		}
	}
	return nil
}

func validateRevocations(payload []byte, now time.Time) (*Revocations, error) {
	var doc Revocations
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, errCode(CodeBadField, "invalid json")
	}
	_, exp, err := documentTimes(doc.GeneratedAt, doc.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if doc.Entries == nil {
		return nil, errCode(CodeBadField, "entries")
	}
	seen := map[string]struct{}{}
	for _, e := range doc.Entries {
		if e.ID == "" {
			return nil, errCode(CodeBadField, "entry id")
		}
		if _, ok := seen[e.ID]; ok {
			return nil, errCode(CodeBadField, "duplicate entry id")
		}
		seen[e.ID] = struct{}{}
		if err := validateRevocationEntry(e); err != nil {
			return nil, err
		}
	}
	if pastExpiry(exp, now) {
		return nil, errCode(CodeExpired, "revocations have expired")
	}
	return &doc, nil
}

func validateRevocationEntry(e RevocationEntry) error {
	switch e.Target.Type {
	case "version":
		if e.Target.PluginID == "" {
			return errCode(CodeBadField, "pluginId")
		}
		if _, err := ParseVersion(e.Target.Version); err != nil {
			return err
		}
		if !isSHA256Hex(e.Target.PackageDigest) {
			return errCode(CodeBadField, "packageDigest")
		}
	case "plugin":
		if e.Target.PluginID == "" {
			return errCode(CodeBadField, "pluginId")
		}
	case "developerKey":
		if e.Target.Fingerprint == "" {
			return errCode(CodeBadField, "fingerprint")
		}
	default:
		return errCode(CodeBadField, "target type")
	}
	switch e.Level {
	case "security", "deprecated":
	default:
		return errCode(CodeBadField, "level")
	}
	if err := maxRunes("reason", e.Reason, 500); err != nil {
		return err
	}
	if _, err := parseTime(e.At); err != nil {
		return err
	}
	if e.RollbackTo != "" {
		if _, err := ParseVersion(e.RollbackTo); err != nil {
			return err
		}
	}
	return nil
}

func validateAttestation(payload []byte) (*Attestation, error) {
	var att Attestation
	if err := json.Unmarshal(payload, &att); err != nil {
		return nil, errCode(CodeBadField, "invalid json")
	}
	if _, err := ParseVersion(att.Version); err != nil {
		return nil, err
	}
	if !isSHA256Hex(att.PackageDigest) {
		return nil, errCode(CodeBadField, "packageDigest")
	}
	if att.PluginID == "" || att.DeveloperFingerprint == "" {
		return nil, errCode(CodeBadField, "attestation field")
	}
	if _, err := parseTime(att.IssuedAt); err != nil {
		return nil, err
	}
	if err := requireSorted("hosts", att.Hosts); err != nil {
		return nil, err
	}
	if err := requireSorted("permissions", att.Permissions); err != nil {
		return nil, err
	}
	return &att, nil
}

func requireSorted(name string, list []string) error {
	if list == nil {
		return errCode(CodeBadField, "missing "+name)
	}
	for i := 1; i < len(list); i++ {
		if list[i] <= list[i-1] {
			return errCode(CodeBadField, name+" must be strictly ascending")
		}
	}
	return nil
}

func maxRunes(name, s string, n int) error {
	if utf8.RuneCountInString(s) > n {
		return errCode(CodeBadField, name+" is too long")
	}
	return nil
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	ma := map[string]struct{}{}
	for _, s := range a {
		ma[s] = struct{}{}
	}
	mb := map[string]struct{}{}
	for _, s := range b {
		mb[s] = struct{}{}
	}
	if len(ma) != len(mb) {
		return false
	}
	for k := range ma {
		if _, ok := mb[k]; !ok {
			return false
		}
	}
	return true
}
