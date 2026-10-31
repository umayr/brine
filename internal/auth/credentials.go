package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Credentials are the admin login and API token Brine is started with.
// When no password/token is supplied, they are generated once and persisted
// so that restarts do not lock the operator out.
type Credentials struct {
	User     string `json:"user"`
	Password string `json:"password"`
	APIToken string `json:"api_token"`
}

// CredentialsPath returns the path of the persisted credentials file.
func CredentialsPath(dataDir string) string {
	return filepath.Join(dataDir, "credentials.json")
}

// LoadOrCreate resolves credentials from explicit values, an existing
// credentials file, or freshly generated secrets. It returns the credentials,
// whether the file was newly created, and its path. The path is empty when
// explicit credentials were supplied and nothing was persisted.
func LoadOrCreate(dataDir, user, password, token string) (Credentials, bool, string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return Credentials{}, false, "", fmt.Errorf("data dir is required")
	}
	if strings.TrimSpace(user) == "" {
		user = "admin"
	}
	// Explicit password and token need no persistence at all.
	if password != "" && token != "" {
		return Credentials{User: user, Password: password, APIToken: token}, false, "", nil
	}
	path := CredentialsPath(dataDir)

	var creds Credentials
	exists := false
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &creds); err != nil {
			return Credentials{}, false, "", fmt.Errorf("parse %s: %w", path, err)
		}
		exists = true
	} else if !os.IsNotExist(err) {
		return Credentials{}, false, "", fmt.Errorf("read %s: %w", path, err)
	}

	// Explicit flags/env win over the file.
	if strings.TrimSpace(user) != "" {
		creds.User = user
	}
	if creds.User == "" {
		creds.User = "admin"
	}
	if password != "" {
		creds.Password = password
	}
	if token != "" {
		creds.APIToken = token
	}

	generated := false
	if creds.Password == "" {
		pw, err := randomSecret(24)
		if err != nil {
			return Credentials{}, false, "", err
		}
		creds.Password = pw
		generated = true
	}
	if creds.APIToken == "" {
		tok, err := randomSecret(32)
		if err != nil {
			return Credentials{}, false, "", err
		}
		creds.APIToken = tok
		generated = true
	}

	// Persist when the file is new or when we filled in a missing secret.
	if !exists || generated {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return Credentials{}, false, "", fmt.Errorf("create data dir: %w", err)
		}
		if err := os.Chmod(dataDir, 0o700); err != nil {
			return Credentials{}, false, "", fmt.Errorf("chmod data dir: %w", err)
		}
		data, err := json.MarshalIndent(creds, "", "  ")
		if err != nil {
			return Credentials{}, false, "", err
		}
		if err := writePrivate(path, append(data, '\n')); err != nil {
			return Credentials{}, false, "", fmt.Errorf("write credentials: %w", err)
		}
		return creds, !exists, path, nil
	}
	// Tighten permissions on files created by older builds.
	_ = os.Chmod(path, 0o600)
	return creds, false, path, nil
}

func writePrivate(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func randomSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
