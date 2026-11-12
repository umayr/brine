package dill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
	"unicode/utf8"
)

// ErrSecretCapabilities indicates that the installed Dill cannot be used for
// the Alpha secret workflow. No subprocess output is included in this error.
var ErrSecretCapabilities = errors.New("secret workflow unavailable: install a compatible Dill candidate")

// CheckSecretCapabilities verifies the static Dill contract before any secret
// workflow is exposed. Discovery is not a readiness or availability check.
func (r *Runner) CheckSecretCapabilities(ctx context.Context, engine string) error {
	if engine != "docker" && engine != "podman" {
		return ErrSecretCapabilities
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Binary, "secret-capabilities", "--format", "json")
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	var output boundedCapabilityOutput
	cmd.Stdout = &output
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil || !validSecretCapabilities(output.Bytes(), engine) {
		return ErrSecretCapabilities
	}
	return nil
}

type boundedCapabilityOutput struct{ bytes.Buffer }

func (b *boundedCapabilityOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8192 {
		return 0, errors.New("capability output too large")
	}
	return b.Buffer.Write(p)
}

func validSecretCapabilities(data []byte, engine string) bool {
	if !utf8.Valid(data) {
		return false
	}
	// Token decoding makes duplicate keys an error instead of accepting the last
	// one, which would make ambiguous discovery responses appear compatible.
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	var contract, bindings, policy string
	var engines []string
	for dec.More() {
		tok, err = dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok || seen[key] {
			return false
		}
		seen[key] = true
		switch key {
		case "contract":
			err = dec.Decode(&contract)
		case "bindings":
			err = dec.Decode(&bindings)
		case "policy":
			err = dec.Decode(&policy)
		case "engines":
			err = dec.Decode(&engines)
		default:
			return false
		}
		if err != nil {
			return false
		}
	}
	if tok, err = dec.Token(); err != nil || tok != json.Delim('}') {
		return false
	}
	if _, err = dec.Token(); err != io.EOF {
		return false
	}
	if len(seen) != 4 || contract != "dill.alpha-secrets/1" || bindings != "dill.secret-bindings/1" || policy != "dill.secret-policy/1" {
		return false
	}
	for _, item := range engines {
		if item == engine+"-host-file" {
			return true
		}
	}
	return false
}
