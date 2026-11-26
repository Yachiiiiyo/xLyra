package credential

import "testing"

func TestServiceEncryptDecryptRoundTripAndMasksSecret(t *testing.T) {
	t.Parallel()

	service := NewService("test-master-key")
	encrypted, masked, err := service.Encrypt("  sk-abcdefghijklmnopqrstuvwxyz  ")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if encrypted == "" || encrypted == "sk-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("expected encrypted secret, got %q", encrypted)
	}
	if masked != "sk-a...wxyz" {
		t.Fatalf("masked = %q, want sk-a...wxyz", masked)
	}

	plain, err := service.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if plain != "sk-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("plain = %q", plain)
	}
}

func TestServiceEncryptRejectsEmptySecret(t *testing.T) {
	t.Parallel()

	if _, _, err := NewService("test-master-key").Encrypt(" \t "); err == nil {
		t.Fatal("expected empty secret to be rejected")
	}
}

func TestServiceEncryptAllowEmptyRoundTrip(t *testing.T) {
	t.Parallel()

	service := NewService("test-master-key")
	encrypted, masked, err := service.EncryptAllowEmpty(" \t ")
	if err != nil {
		t.Fatalf("EncryptAllowEmpty empty: %v", err)
	}
	if encrypted == "" {
		t.Fatal("expected ciphertext for empty secret")
	}
	if masked != "" {
		t.Fatalf("masked = %q, want empty", masked)
	}
	plain, err := service.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if plain != "" {
		t.Fatalf("plain = %q, want empty", plain)
	}

	encrypted2, masked2, err := service.EncryptAllowEmpty("  sk-1234567890  ")
	if err != nil {
		t.Fatalf("EncryptAllowEmpty non-empty: %v", err)
	}
	if masked2 != "sk-1...7890" {
		t.Fatalf("masked2 = %q, want sk-1...7890", masked2)
	}
	plain2, err := service.Decrypt(encrypted2)
	if err != nil || plain2 != "sk-1234567890" {
		t.Fatalf("Decrypt non-empty = (%q, %v), want sk-1234567890", plain2, err)
	}
}
