package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MasterKey holds the 32-byte secret key used for AES-256-GCM encryption.
type MasterKey struct {
	key []byte
}

// SecretEnvelope represents the JSON structure stored in system_settings for secrets.
type SecretEnvelope struct {
	Version int    `json:"v"`
	Nonce   string `json:"nonce"`
	Cipher  string `json:"cipher"`
	Tag     string `json:"tag"`
}

// LoadOrCreateMasterKey loads the master key from the specified file or creates one if it doesn't exist.
func LoadOrCreateMasterKey(path string) (*MasterKey, error) {
	if path == "" {
		return nil, errors.New("master key file path is empty")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve master key path: %w", err)
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(absPath), 0700); err != nil {
			return nil, fmt.Errorf("create master key dir: %w", err)
		}
		// Generate 32 random bytes
		raw := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, raw); err != nil {
			return nil, fmt.Errorf("generate random master key: %w", err)
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		if err := os.WriteFile(absPath, []byte(encoded+"\n"), 0600); err != nil {
			return nil, fmt.Errorf("write master key file: %w", err)
		}
		return &MasterKey{key: raw}, nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read master key file: %w", err)
	}

	trimmed := strings.TrimSpace(string(data))
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 in master key file: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("invalid master key length: expected 32 bytes, got %d", len(raw))
	}

	return &MasterKey{key: raw}, nil
}

// NewMasterKeyFromBytes creates a MasterKey directly from 32 bytes (useful for testing).
func NewMasterKeyFromBytes(raw []byte) (*MasterKey, error) {
	if len(raw) != 32 {
		return nil, fmt.Errorf("expected 32 bytes, got %d", len(raw))
	}
	k := make([]byte, 32)
	copy(k, raw)
	return &MasterKey{key: k}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM with AAD = key + ":" + version.
func (m *MasterKey) Encrypt(settingKey string, plaintext []byte) (string, error) {
	if m == nil || len(m.key) != 32 {
		return "", errors.New("master key is not initialized")
	}

	block, err := aes.NewCipher(m.key)
	if err != nil {
		return "", fmt.Errorf("new aes cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	version := 1
	aad := []byte(fmt.Sprintf("%s:%d", settingKey, version))
	sealed := gcm.Seal(nil, nonce, plaintext, aad)

	// GCM tag is the last 16 bytes of sealed
	tagSize := gcm.Overhead()
	cipherText := sealed[:len(sealed)-tagSize]
	tag := sealed[len(sealed)-tagSize:]

	env := SecretEnvelope{
		Version: version,
		Nonce:   base64.StdEncoding.EncodeToString(nonce),
		Cipher:  base64.StdEncoding.EncodeToString(cipherText),
		Tag:     base64.StdEncoding.EncodeToString(tag),
	}

	bytes, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("marshal secret envelope: %w", err)
	}
	return string(bytes), nil
}

// Decrypt decrypts a JSON secret envelope using AES-256-GCM and verifies AAD.
func (m *MasterKey) Decrypt(settingKey string, envelopeJSON string) ([]byte, error) {
	if m == nil || len(m.key) != 32 {
		return nil, errors.New("master key is not initialized")
	}

	var env SecretEnvelope
	if err := json.Unmarshal([]byte(envelopeJSON), &env); err != nil {
		return nil, fmt.Errorf("unmarshal secret envelope: %w", err)
	}

	if env.Version != 1 {
		return nil, fmt.Errorf("unsupported envelope version: %d", env.Version)
	}

	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}

	cipherText, err := base64.StdEncoding.DecodeString(env.Cipher)
	if err != nil {
		return nil, fmt.Errorf("decode cipher: %w", err)
	}

	tag, err := base64.StdEncoding.DecodeString(env.Tag)
	if err != nil {
		return nil, fmt.Errorf("decode tag: %w", err)
	}

	block, err := aes.NewCipher(m.key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}

	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid nonce size: %d", len(nonce))
	}

	sealed := append(cipherText, tag...)
	aad := []byte(fmt.Sprintf("%s:%d", settingKey, env.Version))

	plain, err := gcm.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret failed (auth tag mismatch or wrong key): %w", err)
	}

	return plain, nil
}
