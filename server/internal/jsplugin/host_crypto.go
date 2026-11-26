package jsplugin

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"strings"
	"unicode/utf8"
)

func bytesToUTF8(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", errors.New("bytes are not valid utf-8")
	}
	return string(data), nil
}

func bytesFromHex(text string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(text))
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}
	if len(decoded) > maxBinaryInputBytes {
		return nil, fmt.Errorf("decoded bytes exceed %d bytes", maxBinaryInputBytes)
	}
	return nonNilBytes(decoded), nil
}

func bytesToHex(data []byte) (string, error) {
	return hex.EncodeToString(data), nil
}

func bytesFromBase64(text string) ([]byte, error) {
	trimmed := strings.TrimSpace(text)
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid base64: %w", err)
		}
	}
	if len(decoded) > maxBinaryInputBytes {
		return nil, fmt.Errorf("decoded bytes exceed %d bytes", maxBinaryInputBytes)
	}
	return nonNilBytes(decoded), nil
}

func bytesToBase64(data []byte) (string, error) {
	return base64.StdEncoding.EncodeToString(data), nil
}

func bytesFromBase64URL(text string) ([]byte, error) {
	trimmed := strings.TrimSpace(text)
	decoded, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid base64url: %w", err)
		}
	}
	if len(decoded) > maxBinaryInputBytes {
		return nil, fmt.Errorf("decoded bytes exceed %d bytes", maxBinaryInputBytes)
	}
	return nonNilBytes(decoded), nil
}

func bytesToBase64URL(data []byte) (string, error) {
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func bytesEqualConstantTime(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

func encodeDigestOutput(sum []byte, outEncoding string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(outEncoding)) {
	case "", "hex":
		return hex.EncodeToString(sum), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(sum), nil
	case "base64url":
		return base64.RawURLEncoding.EncodeToString(sum), nil
	default:
		return "", fmt.Errorf("unsupported digest encoding %q", outEncoding)
	}
}

func newHashByName(alg string) (func() hash.Hash, error) {
	switch strings.ToLower(strings.TrimSpace(alg)) {
	case "md5":
		return md5.New, nil
	case "sha1", "sha-1":
		return sha1.New, nil
	case "sha256", "sha-256":
		return sha256.New, nil
	case "sha512", "sha-512":
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("unsupported hash algorithm %q", alg)
	}
}

func digestBytes(alg string, data []byte) ([]byte, error) {
	fn, err := newHashByName(alg)
	if err != nil {
		return nil, err
	}
	h := fn()
	_, _ = h.Write(data)
	return h.Sum(nil), nil
}

func digestEncoded(alg string, data []byte, outEncoding string) (string, error) {
	sum, err := digestBytes(alg, data)
	if err != nil {
		return "", err
	}
	return encodeDigestOutput(sum, outEncoding)
}

func hmacDigestBytes(alg string, key, data []byte) ([]byte, error) {
	fn, err := newHashByName(alg)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(fn, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil), nil
}

func hmacDigestEncoded(alg string, key, data []byte, outEncoding string) (string, error) {
	sum, err := hmacDigestBytes(alg, key, data)
	if err != nil {
		return "", err
	}
	return encodeDigestOutput(sum, outEncoding)
}

func cryptoRandomBytes(length int) ([]byte, error) {
	if length < 0 || length > maxRandomBytes {
		return nil, fmt.Errorf("randomBytes length must be between 0 and %d", maxRandomBytes)
	}
	if length == 0 {
		return []byte{}, nil
	}
	out := make([]byte, length)
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return out, nil
}

func parseAESMode(alg string) (keyLen int, gcm bool, err error) {
	switch strings.ToLower(strings.TrimSpace(alg)) {
	case "aes-128-gcm":
		return 16, true, nil
	case "aes-256-gcm":
		return 32, true, nil
	case "aes-128-cbc":
		return 16, false, nil
	case "aes-256-cbc":
		return 32, false, nil
	default:
		return 0, false, fmt.Errorf("unsupported AES algorithm %q", alg)
	}
}

func aesEncrypt(alg string, key, iv, plaintext, aad []byte) ([]byte, error) {
	if len(plaintext) > maxBinaryInputBytes {
		return nil, fmt.Errorf("plaintext exceeds %d bytes", maxBinaryInputBytes)
	}
	wantKeyLen, isGCM, err := parseAESMode(alg)
	if err != nil {
		return nil, err
	}
	if len(key) != wantKeyLen {
		return nil, fmt.Errorf("%s requires a %d-byte key, got %d", alg, wantKeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if isGCM {
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		if len(iv) != aead.NonceSize() {
			return nil, fmt.Errorf("%s requires a %d-byte iv/nonce, got %d", alg, aead.NonceSize(), len(iv))
		}
		return nonNilBytes(aead.Seal(nil, iv, plaintext, aad)), nil
	}
	if len(aad) > 0 {
		return nil, fmt.Errorf("%s does not accept aad", alg)
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("%s requires a %d-byte iv, got %d", alg, aes.BlockSize, len(iv))
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func aesDecrypt(alg string, key, iv, ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) > maxBinaryInputBytes+aes.BlockSize {
		return nil, fmt.Errorf("ciphertext exceeds %d bytes", maxBinaryInputBytes)
	}
	wantKeyLen, isGCM, err := parseAESMode(alg)
	if err != nil {
		return nil, err
	}
	if len(key) != wantKeyLen {
		return nil, fmt.Errorf("%s requires a %d-byte key, got %d", alg, wantKeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if isGCM {
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		if len(iv) != aead.NonceSize() {
			return nil, fmt.Errorf("%s requires a %d-byte iv/nonce, got %d", alg, aead.NonceSize(), len(iv))
		}
		plain, err := aead.Open(nil, iv, ciphertext, aad)
		if err != nil {
			return nil, errors.New("aes-gcm authentication failed")
		}
		return nonNilBytes(plain), nil
	}
	if len(aad) > 0 {
		return nil, fmt.Errorf("%s does not accept aad", alg)
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("%s requires a %d-byte iv, got %d", alg, aes.BlockSize, len(iv))
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("aes-cbc ciphertext is not a multiple of block size")
	}
	buf := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(buf, ciphertext)
	plain, err := pkcs7Unpad(buf, aes.BlockSize)
	if err != nil {
		return nil, err
	}
	return nonNilBytes(plain), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - (len(data) % blockSize)
	return append(bytes.Clone(data), bytes.Repeat([]byte{byte(pad)}, pad)...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid pkcs7 padded block")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("invalid pkcs7 padding")
	}
	valid := 1
	for i := len(data) - pad; i < len(data); i++ {
		valid &= subtle.ConstantTimeByteEq(data[i], byte(pad))
	}
	if valid != 1 {
		return nil, errors.New("invalid pkcs7 padding")
	}
	return bytes.Clone(data[:len(data)-pad]), nil
}

func rsaEncryptPEM(publicKeyPEM string, plaintext []byte, padding string) ([]byte, error) {
	pub, err := parseRSAPublicKeyPEM(publicKeyPEM)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(padding)) {
	case "", "oaep-sha256":
		return rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, plaintext, nil)
	case "oaep-sha1":
		return rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, plaintext, nil)
	case "pkcs1v15":
		return rsa.EncryptPKCS1v15(rand.Reader, pub, plaintext)
	default:
		return nil, fmt.Errorf("unsupported RSA padding %q", padding)
	}
}

func parseRSAPublicKeyPEM(raw string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil {
		return nil, errors.New("invalid RSA public key PEM")
	}
	switch block.Type {
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse public key: %w", err)
		}
		pub, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("PEM key is not an RSA public key")
		}
		if pub.N.BitLen() < 1024 {
			return nil, errors.New("RSA key must be at least 1024 bits")
		}
		return pub, nil
	case "RSA PUBLIC KEY":
		pub, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#1 RSA public key: %w", err)
		}
		if pub.N.BitLen() < 1024 {
			return nil, errors.New("RSA key must be at least 1024 bits")
		}
		return pub, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
}

func nonNilBytes(data []byte) []byte {
	if data == nil {
		return []byte{}
	}
	return bytes.Clone(data)
}
