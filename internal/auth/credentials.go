package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Credentials are the admin login and API token Brine is started with.
// When no password/token is supplied, they are generated once and persisted.
type Credentials struct {
	User     string `json:"user"`
	Password string `json:"password"`
	APIToken string `json:"api_token"`
}

func CredentialsPath(dataDir string) string {
	return filepath.Join(dataDir, "credentials.json")
}

// NewWithCredentials resolves startup credentials and enables in-app management
// only for fields supplied by the private file, not flags or environment.
func NewWithCredentials(cfg Config, dataDir string) (*Authenticator, bool, string, error) {
	if cfg.NoAuth {
		a, err := New(cfg)
		return a, false, "", err
	}
	loaded, err := loadCredentials(dataDir, cfg.User, cfg.Password, cfg.APIToken)
	if err != nil {
		return nil, false, "", err
	}
	managed := ManagedFields{
		Username: strings.TrimSpace(cfg.User) == "",
		Password: cfg.Password == "",
		APIToken: cfg.APIToken == "",
	}
	cfg.User, cfg.Password, cfg.APIToken = loaded.creds.User, loaded.creds.Password, loaded.creds.APIToken
	a, err := New(cfg)
	if err != nil {
		return nil, false, "", err
	}
	if loaded.path != "" {
		a.management = &credentialManagement{dir: filepath.Dir(loaded.path), fields: managed, digest: loaded.digest}
	}
	return a, loaded.created, loaded.path, nil
}

// LoadOrCreate resolves credentials without enabling runtime management.
func LoadOrCreate(dataDir, user, password, token string) (Credentials, bool, string, error) {
	loaded, err := loadCredentials(dataDir, user, password, token)
	return loaded.creds, loaded.created, loaded.path, err
}

type loadedCredentials struct {
	creds   Credentials
	created bool
	path    string
	digest  [sha256.Size]byte
}

func loadCredentials(dataDir, user, password, token string) (loadedCredentials, error) {
	if strings.TrimSpace(dataDir) == "" {
		return loadedCredentials{}, fmt.Errorf("data dir is required")
	}
	if strings.TrimSpace(user) == "" {
		user = ""
	}
	// Explicit password and token need no persistence at all.
	if password != "" && token != "" {
		if user == "" {
			user = "admin"
		}
		return loadedCredentials{creds: Credentials{User: user, Password: password, APIToken: token}}, nil
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return loadedCredentials{}, fmt.Errorf("resolve credential directory")
	}
	state, err := openCredentialState(abs)
	if err != nil {
		return loadedCredentials{}, err
	}
	defer state.close()
	data, exists, err := state.read()
	if err != nil {
		return loadedCredentials{}, err
	}
	var creds Credentials
	if exists && json.Unmarshal(data, &creds) != nil {
		return loadedCredentials{}, fmt.Errorf("invalid credential document")
	}
	// Explicit flags/env win over the file.
	if user != "" {
		creds.User = user
	}
	if strings.TrimSpace(creds.User) == "" {
		creds.User = "admin"
	}
	if password != "" {
		creds.Password = password
	}
	if token != "" {
		creds.APIToken = token
	}

	generated := creds.Password == "" || creds.APIToken == ""
	if creds.Password == "" {
		creds.Password, err = randomSecret(24)
		if err != nil {
			return loadedCredentials{}, err
		}
	}
	if creds.APIToken == "" {
		creds.APIToken, err = randomSecret(32)
		if err != nil {
			return loadedCredentials{}, err
		}
	}

	// Preserve existing file behavior: explicit overrides on a complete
	// document are not written back as a side effect of startup.
	if !exists || generated {
		data, err = encodeCredentials(creds)
		if err != nil {
			return loadedCredentials{}, err
		}
		if err = state.write(data); err != nil {
			return loadedCredentials{}, err
		}
	}
	return loadedCredentials{creds: creds, created: !exists, path: CredentialsPath(abs), digest: sha256.Sum256(data)}, nil
}

func encodeCredentials(creds Credentials) ([]byte, error) {
	data, err := json.MarshalIndent(creds, "", "  ")
	return append(data, '\n'), err
}

func randomSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
