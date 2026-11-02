package cloudclient

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
)

const signPrefix = "xlyra-instance-v1"

// CanonicalMessage is the exact byte string an instance signs.
// An empty body hashes as the empty string. The path does not include a query.
func CanonicalMessage(method, path string, unixSeconds int64, body []byte) string {
	sum := sha256.Sum256(body)
	return signPrefix + "\n" +
		strings.ToUpper(method) + "\n" +
		path + "\n" +
		strconv.FormatInt(unixSeconds, 10) + "\n" +
		hex.EncodeToString(sum[:])
}

// Sign returns the standard base64 ed25519 signature of message.
func Sign(privateKey ed25519.PrivateKey, message string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(message)))
}
