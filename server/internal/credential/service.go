package credential

import (
	"fmt"
	"strings"

	"xlyra/server/internal/crypto"
)

type Service struct {
	masterKey string
}

func NewService(masterKey string) Service {
	return Service{masterKey: masterKey}
}

func (s Service) Encrypt(secret string) (encrypted string, masked string, err error) {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return "", "", fmt.Errorf("secret must not be empty")
	}

	encrypted, err = crypto.EncryptSecret(s.masterKey, trimmed)
	if err != nil {
		return "", "", err
	}

	return encrypted, crypto.MaskSecret(trimmed), nil
}

// EncryptAllowEmpty encrypts an intentionally empty secret (used only by
// plugin api_key site types that explicitly declare credential.allowEmpty).
// Non-empty secrets behave identically to Encrypt.
func (s Service) EncryptAllowEmpty(secret string) (encrypted string, masked string, err error) {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		encrypted, err = crypto.EncryptSecret(s.masterKey, "")
		if err != nil {
			return "", "", err
		}
		return encrypted, "", nil
	}

	return s.Encrypt(trimmed)
}

func (s Service) Decrypt(encrypted string) (string, error) {
	return crypto.DecryptSecret(s.masterKey, encrypted)
}
