package marketproto

import (
	"crypto/ed25519"
	"encoding/json"
)

// VerifyKeyTransition checks a key-rotation statement signed by oldPub.
// oldFingerprint must be KeyID(oldPub) and newFingerprint must be KeyID of newPublicKey.
func VerifyKeyTransition(raw []byte, oldPub ed25519.PublicKey) (*KeyTransition, error) {
	env, payload, err := parseEnvelope(raw, PayloadTypeKeyTransition)
	if err != nil {
		return nil, err
	}
	if err := evalSigs(env.PayloadType, payload, env.Signatures, func(id string) (ed25519.PublicKey, sigClass) {
		if len(oldPub) == ed25519.PublicKeySize && KeyID(oldPub) == id {
			return oldPub, sigAccept
		}
		return nil, sigIgnore
	}, nil); err != nil {
		return nil, err
	}
	if err := checkV(payload); err != nil {
		return nil, err
	}
	var kt KeyTransition
	if err := json.Unmarshal(payload, &kt); err != nil {
		return nil, errCode(CodeBadField, "invalid json")
	}
	if kt.Namespace == "" {
		return nil, errCode(CodeBadField, "namespace")
	}
	if kt.OldFingerprint != KeyID(oldPub) {
		return nil, errCode(CodeBadField, "oldFingerprint")
	}
	pub, err := ParsePublicKey(kt.NewPublicKey)
	if err != nil {
		return nil, err
	}
	if kt.NewFingerprint != KeyID(pub) {
		return nil, errCode(CodeBadField, "newFingerprint")
	}
	if _, err := parseTime(kt.IssuedAt); err != nil {
		return nil, err
	}
	return &kt, nil
}
