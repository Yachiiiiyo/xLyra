package marketproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
)

const (
	RoleIndex       = "index"
	RoleRevocations = "revocations"
	RoleAttestation = "attestation"
)

// KeyID is "SHA256:" plus the standard base64 of sha256(pub).
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:])
}

// ParsePublicKey decodes a standard-base64 ed25519 public key.
// The decoded length must be 32 bytes.
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errCode(CodeBadField, "public key")
	}
	return ed25519.PublicKey(raw), nil
}
