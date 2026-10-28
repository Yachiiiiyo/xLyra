package marketproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
)

const (
	PayloadTypeRoot          = "application/vnd.xlyra.root+json"
	PayloadTypeIndex         = "application/vnd.xlyra.index+json"
	PayloadTypeRevocations   = "application/vnd.xlyra.revocations+json"
	PayloadTypeAttestation   = "application/vnd.xlyra.attestation+json"
	PayloadTypeKeyTransition = "application/vnd.xlyra.key-transition+json"
)

// Envelope is the DSSE-style container around a signed payload.
type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

// Signature is one ed25519 signature over PAE(payloadType, payload).
type Signature struct {
	KeyID string `json:"keyId"`
	Sig   string `json:"sig"`
}

// PAE is the DSSE v1 pre-authentication encoding.
//
//	DSSEv1 <len(payloadType)> <payloadType> <len(payload)> <payload>
//
// Lengths are decimal byte counts.
func PAE(payloadType string, payload []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	b.Write(payload)
	return b.Bytes()
}

// Sign builds a single-signature envelope over payload.
func Sign(payloadType string, payload []byte, priv ed25519.PrivateKey) Envelope {
	pub := priv.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(priv, PAE(payloadType, payload))
	return Envelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures: []Signature{{
			KeyID: KeyID(pub),
			Sig:   base64.StdEncoding.EncodeToString(sig),
		}},
	}
}
