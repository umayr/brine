package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/config"
)

func TestGeneratedCredentialsNotLogged(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.AuthPassword = ""
	cfg.APIToken = ""
	if _, err := buildAuth(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.DataDir + "/credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"password:", "api token:"} {
		if strings.Contains(output.String(), marker) {
			t.Fatal("credentials logged")
		}
	}
	var creds struct {
		Password string `json:"password"`
		APIToken string `json:"api_token"`
	}
	if err := json.Unmarshal(raw, &creds); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{creds.Password, creds.APIToken} {
		if secret == "" || strings.Contains(output.String(), secret) {
			t.Fatal("missing or logged credential")
		}
	}
}
