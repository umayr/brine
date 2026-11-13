package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	state, err := openCredentialState(dataDir)
	if err != nil {
		return Credentials{}, false, "", err
	}
	defer state.close()
	data, exists, err := state.read()
	if err != nil {
		return Credentials{}, false, "", err
	}
	var creds Credentials
	if exists && json.Unmarshal(data, &creds) != nil {
		// Neither filesystem paths nor document contents belong in diagnostics.
		return Credentials{}, false, "", fmt.Errorf("invalid credential document")
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
		data, err := json.MarshalIndent(creds, "", "  ")
		if err != nil {
			return Credentials{}, false, "", err
		}
		if err := state.write(append(data, '\n')); err != nil {
			return Credentials{}, false, "", err
		}
		return creds, !exists, path, nil
	}
	return creds, false, path, nil
}

func randomSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
