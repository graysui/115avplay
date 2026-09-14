package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMasterKeyEncryptDecrypt(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, "master.key")

	mk, err := LoadOrCreateMasterKey(keyFile)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey failed: %v", err)
	}

	settingKey := "provider_tokens:115"
	original := []byte(`{"access_token":"test_token_123","refresh_token":"ref_456"}`)

	encrypted, err := mk.Encrypt(settingKey, original)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decrypted, err := mk.Decrypt(settingKey, encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if string(decrypted) != string(original) {
		t.Fatalf("expected %s, got %s", string(original), string(decrypted))
	}

	// Test with wrong settingKey (AAD mismatch)
	_, err = mk.Decrypt("other_setting_key", encrypted)
	if err == nil {
		t.Fatalf("expected error on AAD mismatch, got nil")
	}

	// Test with a different key
	mk2, _ := NewMasterKeyFromBytes(make([]byte, 32))
	_, err = mk2.Decrypt(settingKey, encrypted)
	if err == nil {
		t.Fatalf("expected error on wrong key, got nil")
	}
}

func TestConfigSettingValidation(t *testing.T) {
	if err := ValidateSetting("temp_transfer_cid", "0"); err == nil {
		t.Fatalf("expected error for temp_transfer_cid=0")
	}
	if err := ValidateSetting("existing_scan_interval_min", "10"); err == nil {
		t.Fatalf("expected error for existing_scan_interval_min=10 (min 15)")
	}
	if err := ValidateSetting("existing_scan_interval_min", "60"); err != nil {
		t.Fatalf("unexpected error for existing_scan_interval_min=60: %v", err)
	}
	if err := ValidateSetting("queue_limit", "1000"); err != nil {
		t.Fatalf("unexpected error for queue_limit=1000: %v", err)
	}
}

func TestBootstrapPasswordGeneration(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &AppConfig{
		DataDir: tempDir,
	}

	pwd1, isNew1, err := cfg.GetOrGenerateAdminBootstrapPassword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew1 || pwd1 == "" {
		t.Fatalf("expected new bootstrap password")
	}

	pwd2, isNew2, err := cfg.GetOrGenerateAdminBootstrapPassword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew2 || pwd2 != pwd1 {
		t.Fatalf("expected same bootstrap password across calls, got %s vs %s", pwd1, pwd2)
	}

	if err := cfg.RemoveBootstrapPasswordFile(); err != nil {
		t.Fatalf("failed to remove bootstrap file: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tempDir, "bootstrap-password")); !os.IsNotExist(err) {
		t.Fatalf("expected bootstrap file to be removed")
	}
}
