package ingestion

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

var (
	ErrManifestNotFound       = errors.New("outer zip does not contain manifest")
	ErrPayloadNotFound        = errors.New("outer zip does not contain payload binary")
	ErrDecryptFailed          = errors.New("decryption failed: authentication tag mismatch or corrupted archive")
	ErrInnerDataNotFound      = errors.New("inner decrypted zip does not contain csv/data file")
	ErrArchiveTooLarge        = errors.New("archive size exceeds allowed budget")
	ErrExpandedArchiveTooLarge = errors.New("expanded archive size exceeds allowed budget")
	ErrIterationLimitExceeded = errors.New("pbkdf2 iterations exceed allowed limit")
	ErrZipSlipAttempt         = errors.New("illegal zip path detected (zip slip)")
)

// DefaultResourceLibraryPasswordDigest is the fixed 32-byte digest for AVDB release archives.
var DefaultResourceLibraryPasswordDigest = []byte{
	0xca, 0x42, 0xe6, 0x87, 0xdf, 0x58, 0x18, 0xe2,
	0xe8, 0x8d, 0xa0, 0xff, 0x5b, 0x9f, 0xd2, 0xc6,
	0x0f, 0x7e, 0x22, 0x72, 0x1f, 0x68, 0x2b, 0x66,
	0xc3, 0xe5, 0x04, 0x85, 0xa0, 0x0d, 0x06, 0xd5,
}

const ManifestFilename = "avdb-resource-library.json"

type Manifest struct {
	Payload    string `json:"payload"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Tag        string `json:"tag"`
	Iterations int    `json:"iterations"`
}

type DecryptConfig struct {
	PasswordDigest          []byte
	ArchiveMaxBytes         int64
	ArchiveExpandedMaxBytes int64
	PBKDF2MaxIterations     int
}

func DefaultDecryptConfig() DecryptConfig {
	return DecryptConfig{
		PasswordDigest:          DefaultResourceLibraryPasswordDigest,
		ArchiveMaxBytes:         512 * 1024 * 1024,  // 512 MiB
		ArchiveExpandedMaxBytes: 4 * 1024 * 1024 * 1024, // 4 GiB
		PBKDF2MaxIterations:     1000000,
	}
}

// DecryptAVDBArchive decrypts an AVDB outer zip and extracts the inner CSV/data file.
// Returns the data filename and the uncompressed raw bytes.
func DecryptAVDBArchive(zipData []byte, cfg DecryptConfig) (string, []byte, error) {
	if cfg.ArchiveMaxBytes > 0 && int64(len(zipData)) > cfg.ArchiveMaxBytes {
		return "", nil, fmt.Errorf("%w: %d > %d", ErrArchiveTooLarge, len(zipData), cfg.ArchiveMaxBytes)
	}

	readerAt := bytes.NewReader(zipData)
	outerZip, err := zip.NewReader(readerAt, int64(len(zipData)))
	if err != nil {
		return "", nil, fmt.Errorf("open outer zip: %w", err)
	}

	// 1. Locate manifest and payload or unencrypted data
	var manifestFile *zip.File
	var payloadFile *zip.File
	var unencryptedDataFile *zip.File

	for _, f := range outerZip.File {
		cleanPath := filepath.Clean(f.Name)
		if strings.HasPrefix(cleanPath, "..") || strings.HasPrefix(cleanPath, "/") || strings.HasPrefix(cleanPath, "\\") {
			return "", nil, ErrZipSlipAttempt
		}

		if f.Name == ManifestFilename {
			manifestFile = f
		} else if strings.HasSuffix(strings.ToLower(f.Name), ".csv") ||
			strings.HasSuffix(strings.ToLower(f.Name), ".xlsx") ||
			strings.HasSuffix(strings.ToLower(f.Name), ".xls") {
			unencryptedDataFile = f
		}
	}

	// If unencrypted CSV is present and manifest is absent, extract directly
	if manifestFile == nil {
		if unencryptedDataFile != nil {
			content, err := readZipFileBounded(unencryptedDataFile, cfg.ArchiveExpandedMaxBytes)
			if err != nil {
				return "", nil, err
			}
			return unencryptedDataFile.Name, content, nil
		}
		return "", nil, ErrManifestNotFound
	}

	// 2. Read manifest
	manifestRC, err := manifestFile.Open()
	if err != nil {
		return "", nil, fmt.Errorf("open manifest: %w", err)
	}
	defer manifestRC.Close()

	var manifest Manifest
	if err := json.NewDecoder(manifestRC).Decode(&manifest); err != nil {
		return "", nil, fmt.Errorf("decode manifest: %w", err)
	}

	payloadName := manifest.Payload
	if payloadName == "" {
		payloadName = "avdb-resource-library.bin"
	}

	for _, f := range outerZip.File {
		if f.Name == payloadName {
			payloadFile = f
			break
		}
	}
	if payloadFile == nil {
		return "", nil, fmt.Errorf("%w: %s", ErrPayloadNotFound, payloadName)
	}

	// 3. Validate iteration count
	iterations := manifest.Iterations
	if iterations <= 0 {
		iterations = 200000
	}
	if cfg.PBKDF2MaxIterations > 0 && iterations > cfg.PBKDF2MaxIterations {
		return "", nil, fmt.Errorf("%w: %d > %d", ErrIterationLimitExceeded, iterations, cfg.PBKDF2MaxIterations)
	}

	salt, err := base64.StdEncoding.DecodeString(manifest.Salt)
	if err != nil {
		return "", nil, fmt.Errorf("decode salt: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(manifest.Nonce)
	if err != nil {
		return "", nil, fmt.Errorf("decode nonce: %w", err)
	}
	tag, err := base64.StdEncoding.DecodeString(manifest.Tag)
	if err != nil {
		return "", nil, fmt.Errorf("decode tag: %w", err)
	}

	// Check sizes of nonce and tag
	if len(nonce) != 12 {
		return "", nil, fmt.Errorf("invalid nonce length: %d (expected 12)", len(nonce))
	}
	if len(tag) != 16 {
		return "", nil, fmt.Errorf("invalid tag length: %d (expected 16)", len(tag))
	}

	// Read ciphertext
	ciphertext, err := readZipFileBounded(payloadFile, cfg.ArchiveMaxBytes)
	if err != nil {
		return "", nil, fmt.Errorf("read payload: %w", err)
	}

	// 4. Derive AES-256 Key via PBKDF2-HMAC-SHA256
	pwdDigest := cfg.PasswordDigest
	if len(pwdDigest) == 0 {
		pwdDigest = DefaultResourceLibraryPasswordDigest
	}
	aesKey := pbkdf2.Key(pwdDigest, salt, iterations, 32, sha256.New)

	// 5. AES-256-GCM Decryption (ciphertext || tag)
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", nil, fmt.Errorf("create aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, fmt.Errorf("create gcm: %w", err)
	}

	encryptedPayload := append(ciphertext, tag...)
	innerZipBytes, err := gcm.Open(nil, nonce, encryptedPayload, nil)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrDecryptFailed, err)
	}

	// 6. Inspect inner zip and extract CSV/data
	innerReaderAt := bytes.NewReader(innerZipBytes)
	innerZip, err := zip.NewReader(innerReaderAt, int64(len(innerZipBytes)))
	if err != nil {
		return "", nil, fmt.Errorf("open inner zip: %w", err)
	}

	var bestCandidate *zip.File
	var totalExpanded int64

	for _, f := range innerZip.File {
		cleanPath := filepath.Clean(f.Name)
		if strings.HasPrefix(cleanPath, "..") || strings.HasPrefix(cleanPath, "/") || strings.HasPrefix(cleanPath, "\\") {
			return "", nil, ErrZipSlipAttempt
		}

		totalExpanded += int64(f.UncompressedSize64)
		if cfg.ArchiveExpandedMaxBytes > 0 && totalExpanded > cfg.ArchiveExpandedMaxBytes {
			return "", nil, fmt.Errorf("%w: %d > %d", ErrExpandedArchiveTooLarge, totalExpanded, cfg.ArchiveExpandedMaxBytes)
		}

		lower := strings.ToLower(f.Name)
		if strings.HasSuffix(lower, ".csv") || strings.HasSuffix(lower, ".xlsx") || strings.HasSuffix(lower, ".xls") {
			if bestCandidate == nil {
				bestCandidate = f
			} else if strings.Count(f.Name, "/") < strings.Count(bestCandidate.Name, "/") {
				bestCandidate = f
			}
		}
	}

	if bestCandidate == nil {
		return "", nil, ErrInnerDataNotFound
	}

	dataBytes, err := readZipFileBounded(bestCandidate, cfg.ArchiveExpandedMaxBytes)
	if err != nil {
		return "", nil, err
	}

	return bestCandidate.Name, dataBytes, nil
}

func readZipFileBounded(f *zip.File, maxBytes int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var buf bytes.Buffer
	var limitReader io.Reader = rc
	if maxBytes > 0 {
		limitReader = io.LimitReader(rc, maxBytes+1)
	}

	n, err := io.Copy(&buf, limitReader)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && n > maxBytes {
		return nil, fmt.Errorf("%w: read %d exceeds %d", ErrExpandedArchiveTooLarge, n, maxBytes)
	}

	return buf.Bytes(), nil
}

// Helper to parse hex string into digest
func MustParseHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
