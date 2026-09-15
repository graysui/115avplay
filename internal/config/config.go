package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AppConfig represents startup and runtime environment configuration.
type AppConfig struct {
	DataDir        string
	MasterKeyFile  string
	AdminPassword  string
	Listen         string
	PublicURL      string
	TrustedProxies []string
	LogLevel       string

	// 115 OpenAPI application ID (https://open.115.com).
	// Required for the OAuth device-code / QR-code binding flow.
	// Note: the device flow only needs client_id; no client secret is used.
	Client115ID string

	MasterKey *MasterKey

	// EnvOverrides stores MV_<SETTING_KEY_UPPER> that override database settings
	EnvOverrides map[string]string
}

// LoadFromEnv loads configuration from environment variables and sets defaults according to CONFIGURATION.md.
func LoadFromEnv() (*AppConfig, error) {
	dataDir := getEnvOrDefault("MV_DATA_DIR", "./data")
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir path: %w", err)
	}

	if err := os.MkdirAll(absDataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	masterKeyFile := os.Getenv("MV_MASTER_KEY_FILE")
	if masterKeyFile == "" {
		masterKeyFile = filepath.Join(absDataDir, "master.key")
	}

	adminPassword := os.Getenv("MV_ADMIN_PASSWORD")
	listen := getEnvOrDefault("MV_LISTEN", "127.0.0.1:8096")
	publicURL := strings.TrimRight(os.Getenv("MV_PUBLIC_URL"), "/")
	logLevel := strings.ToUpper(getEnvOrDefault("MV_LOG_LEVEL", "INFO"))

	client115ID := os.Getenv("MV_115_CLIENT_ID")

	var trustedProxies []string
	if rawProxies := os.Getenv("MV_TRUSTED_PROXIES"); rawProxies != "" {
		parts := strings.Split(rawProxies, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				trustedProxies = append(trustedProxies, p)
			}
		}
	}

	// Scan for MV_<SETTING_KEY> env overrides
	overrides := make(map[string]string)
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if len(pair) != 2 {
			continue
		}
		key, val := pair[0], pair[1]
		if strings.HasPrefix(key, "MV_") {
			// Skip startup-specific env vars
			switch key {
			case "MV_DATA_DIR", "MV_MASTER_KEY_FILE", "MV_ADMIN_PASSWORD",
				"MV_LISTEN", "MV_PUBLIC_URL", "MV_TRUSTED_PROXIES", "MV_LOG_LEVEL",
				"MV_115_CLIENT_ID", "MV_115_CLIENT_SECRET":
				continue
			default:
				settingKey := strings.ToLower(strings.TrimPrefix(key, "MV_"))
				overrides[settingKey] = val
			}
		}
	}

	cfg := &AppConfig{
		DataDir:        absDataDir,
		MasterKeyFile:  masterKeyFile,
		AdminPassword:  adminPassword,
		Listen:         listen,
		PublicURL:      publicURL,
		TrustedProxies: trustedProxies,
		LogLevel:       logLevel,
		Client115ID:    client115ID,
		EnvOverrides:   overrides,
	}

	return cfg, nil
}

// InitMasterKey loads or initializes the master key file.
func (c *AppConfig) InitMasterKey() error {
	mk, err := LoadOrCreateMasterKey(c.MasterKeyFile)
	if err != nil {
		return fmt.Errorf("initialize master key: %w", err)
	}
	c.MasterKey = mk
	return nil
}

// GetOrGenerateAdminBootstrapPassword returns the admin password or writes a one-time bootstrap-password file.
func (c *AppConfig) GetOrGenerateAdminBootstrapPassword() (string, bool, error) {
	if c.AdminPassword != "" {
		return c.AdminPassword, false, nil
	}

	bootstrapPath := filepath.Join(c.DataDir, "bootstrap-password")
	if _, err := os.Stat(bootstrapPath); err == nil {
		data, err := os.ReadFile(bootstrapPath)
		if err != nil {
			return "", false, fmt.Errorf("read bootstrap password: %w", err)
		}
		pwd := strings.TrimSpace(string(data))
		if pwd != "" {
			return pwd, true, nil
		}
	}

	// Generate 16 random bytes base64 as one-time password
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", false, fmt.Errorf("generate random password: %w", err)
	}
	pwd := base64.RawURLEncoding.EncodeToString(raw)

	if err := os.WriteFile(bootstrapPath, []byte(pwd+"\n"), 0600); err != nil {
		return "", false, fmt.Errorf("write bootstrap password: %w", err)
	}

	return pwd, true, nil
}

// RemoveBootstrapPasswordFile removes the one-time bootstrap file after password change.
func (c *AppConfig) RemoveBootstrapPasswordFile() error {
	bootstrapPath := filepath.Join(c.DataDir, "bootstrap-password")
	if err := os.Remove(bootstrapPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SettingValidator checks setting types and boundaries according to CONFIGURATION.md.
func ValidateSetting(key, val string) error {
	// An empty value means "not set" (the UI may submit blank inputs); accept it
	// and let consumers fall back to their built-in defaults.
	if strings.TrimSpace(val) == "" {
		return nil
	}

	switch key {
	case "existing_scan_cids":
		var roots []string
		if err := json.Unmarshal([]byte(val), &roots); err != nil {
			return errors.New("existing_scan_cids must be a JSON array of CID strings, e.g. [\"123\"]")
		}
	case "temp_transfer_cid":
		if val == "0" {
			return errors.New("temp_transfer_cid cannot be 0")
		}
	case "existing_scan_interval_min":
		v, err := strconv.Atoi(val)
		if err != nil || v < 15 || v > 1440 {
			return errors.New("existing_scan_interval_min must be integer between 15 and 1440")
		}
	case "full_scan_interval_hours":
		v, err := strconv.Atoi(val)
		if err != nil || v < 1 || v > 24 {
			return errors.New("full_scan_interval_hours must be integer between 1 and 24")
		}
	case "cleanup_enabled", "image_proxy":
		if val != "true" && val != "false" && val != "1" && val != "0" {
			return fmt.Errorf("%s must be boolean", key)
		}
	case "cleanup_ttl_days":
		v, err := strconv.Atoi(val)
		if err != nil || v < 1 || v > 30 {
			return errors.New("cleanup_ttl_days must be integer between 1 and 30")
		}
	case "cleanup_confirm_timeout_sec":
		v, err := strconv.Atoi(val)
		if err != nil || v < 30 || v > 3600 {
			return errors.New("cleanup_confirm_timeout_sec must be integer between 30 and 3600")
		}
	case "playback_lease_sec":
		v, err := strconv.Atoi(val)
		if err != nil || v < 60 || v > 600 {
			return errors.New("playback_lease_sec must be integer between 60 and 600")
		}
	case "transfer_concurrency":
		v, err := strconv.Atoi(val)
		if err != nil || v < 1 || v > 4 {
			return errors.New("transfer_concurrency must be integer between 1 and 4")
		}
	case "transfer_deadline_sec":
		v, err := strconv.Atoi(val)
		if err != nil || v < 300 || v > 86400 {
			return errors.New("transfer_deadline_sec must be integer between 300 and 86400")
		}
	case "stream_wait_ms":
		v, err := strconv.Atoi(val)
		if err != nil || v < 0 || v > 10000 {
			return errors.New("stream_wait_ms must be integer between 0 and 10000")
		}
	case "scrape_concurrency":
		v, err := strconv.Atoi(val)
		if err != nil || v < 1 || v > 2 {
			return errors.New("scrape_concurrency must be integer between 1 and 2")
		}
	case "javdb_request_interval_sec":
		v, err := strconv.ParseFloat(val, 64)
		if err != nil || v < 3.0 {
			return errors.New("javdb_request_interval_sec must be float >= 3.0")
		}
	case "javdb_search_timeout_ms":
		v, err := strconv.Atoi(val)
		if err != nil || v < 1000 || v > 10000 {
			return errors.New("javdb_search_timeout_ms must be integer between 1000 and 10000")
		}
	case "javdb_partial_attempts":
		v, err := strconv.Atoi(val)
		if err != nil || v < 1 || v > 10 {
			return errors.New("javdb_partial_attempts must be integer between 1 and 10")
		}
	case "magnet_refresh_hours":
		v, err := strconv.Atoi(val)
		if err != nil || v < 24 {
			return errors.New("magnet_refresh_hours must be integer >= 24")
		}
	case "queue_limit":
		v, err := strconv.Atoi(val)
		if err != nil || v < 1 || v > 10000 {
			return errors.New("queue_limit must be integer between 1 and 10000")
		}
	case "alert_silence_sec":
		v, err := strconv.Atoi(val)
		if err != nil || v < 60 || v > 86400 {
			return errors.New("alert_silence_sec must be integer between 60 and 86400")
		}
	}
	return nil
}

func getEnvOrDefault(key, defVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defVal
}
