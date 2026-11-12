//go:build unix

package dill

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/umayr/brine/internal/secretbindings"
)

var ErrSecretUnavailable = errors.New("secret configuration or source unavailable")
var ErrSecretApplyFailed = errors.New("checked deployment failed; partial changes may have occurred")

// SecretRunner invokes only the paired Alpha interface. Callers must share the
// coordinator with every operation that can modify a deployment workspace.
type SecretRunner struct {
	Runner                                    *Runner
	State                                     *secretbindings.Store
	Coordinator                               *secretbindings.Coordinator
	Engine, Socket, Policy, Target, PklBinary string
}

func (s *SecretRunner) valid() bool {
	return s != nil && s.Runner != nil && s.State != nil && s.Coordinator != nil && (s.Engine == "docker" || s.Engine == "podman") && filepath.IsAbs(s.Socket) && filepath.Clean(s.Socket) == s.Socket && filepath.IsAbs(s.Policy) && filepath.Clean(s.Policy) == s.Policy && (s.PklBinary == "" || filepath.IsAbs(s.PklBinary))
}
func (s *SecretRunner) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.Runner.executable(), args...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C", "DILL_SECRET_POLICY=" + s.Policy}
	if s.PklBinary != "" {
		cmd.Env = append(cmd.Env, "DILL_PKL_PATH="+s.PklBinary)
	}
	cmd.WaitDelay = time.Second
	return cmd
}

type secretOutput struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (o *secretOutput) Write(p []byte) (int, error) {
	if len(p) > o.max-o.buf.Len() {
		o.overflow = true
		return 0, ErrSecretUnavailable
	}
	return o.buf.Write(p)
}
func (s *SecretRunner) capture(ctx context.Context, args ...string) ([]byte, int, error) {
	return s.captureInput(ctx, nil, args...)
}
func (s *SecretRunner) captureInput(ctx context.Context, input *string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := s.command(ctx, args...)
	if input != nil {
		cmd.Stdin = strings.NewReader(*input)
	}
	out := secretOutput{max: 256 * 1024}
	cmd.Stdout = &out
	err := cmd.Run()
	if out.overflow || ctx.Err() != nil {
		return nil, 0, ErrSecretUnavailable
	}
	if err != nil {
		if !isExitCode(err, 1) {
			return nil, 0, ErrSecretUnavailable
		}
		return out.buf.Bytes(), 1, nil
	}
	return out.buf.Bytes(), 0, nil
}
func strictObject(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, ErrSecretUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(data))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, ErrSecretUnavailable
	}
	keys := map[string]bool{}
	for _, k := range allowed {
		keys[k] = true
	}
	m := map[string]json.RawMessage{}
	for d.More() {
		t, e := d.Token()
		k, ok := t.(string)
		if e != nil || !ok || !keys[k] {
			return nil, ErrSecretUnavailable
		}
		if _, exists := m[k]; exists {
			return nil, ErrSecretUnavailable
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, ErrSecretUnavailable
		}
		m[k] = v
	}
	tok, err = d.Token()
	if err != nil || tok != json.Delim('}') || len(m) != len(allowed) {
		return nil, ErrSecretUnavailable
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrSecretUnavailable
	}
	return m, nil
}
func parseProof(data []byte) (string, error) {
	m, err := strictObject(data, "contract", "proof")
	if err != nil {
		return "", err
	}
	var contract, proof string
	if len(m["contract"]) == 0 || m["contract"][0] != '"' || json.Unmarshal(m["contract"], &contract) != nil || contract != "dill.alpha-secrets-preview/1" || len(m["proof"]) == 0 || m["proof"][0] != '"' || json.Unmarshal(m["proof"], &proof) != nil {
		return "", ErrSecretUnavailable
	}
	b, err := base64.RawURLEncoding.DecodeString(proof)
	if err != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != proof {
		return "", ErrSecretUnavailable
	}
	return proof, nil
}
func parseSecretPlan(data []byte, exit int) (*PlanResult, error) {
	m, err := strictObject(data, "changes", "summary")
	if err != nil || len(m["changes"]) == 0 || m["changes"][0] != '[' || len(m["summary"]) == 0 || m["summary"][0] != '{' {
		return nil, ErrSecretUnavailable
	}
	summary, err := strictObject(m["summary"], "create", "recreate", "remove", "noop")
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	for _, raw := range summary {
		var n int
		if string(raw) == "null" || json.Unmarshal(raw, &n) != nil || n < 0 {
			return nil, ErrSecretUnavailable
		}
	}
	var changes []json.RawMessage
	if json.Unmarshal(m["changes"], &changes) != nil {
		return nil, ErrSecretUnavailable
	}
	for _, raw := range changes {
		change, e := strictObject(raw, "service", "kind", "diffs")
		if e != nil {
			return nil, ErrSecretUnavailable
		}
		for _, key := range []string{"service", "kind"} {
			if len(change[key]) == 0 || change[key][0] != '"' {
				return nil, ErrSecretUnavailable
			}
		}
		if string(change["diffs"]) != "null" {
			if len(change["diffs"]) == 0 || change["diffs"][0] != '[' {
				return nil, ErrSecretUnavailable
			}
			var diffs []json.RawMessage
			if json.Unmarshal(change["diffs"], &diffs) != nil {
				return nil, ErrSecretUnavailable
			}
			for _, diff := range diffs {
				fields, e := strictObject(diff, "field", "before", "after")
				if e != nil {
					return nil, ErrSecretUnavailable
				}
				for _, v := range fields {
					if len(v) == 0 || v[0] != '"' {
						return nil, ErrSecretUnavailable
					}
				}
			}
		}
	}
	var result PlanResult
	if json.Unmarshal(data, &result) != nil {
		return nil, ErrSecretUnavailable
	}
	counts := PlanSummary{}
	for _, c := range result.Changes {
		if c.Service == "" {
			return nil, ErrSecretUnavailable
		}
		switch c.Kind {
		case "create":
			counts.Create++
		case "recreate":
			counts.Recreate++
		case "remove":
			counts.Remove++
		case "noop":
			counts.Noop++
		default:
			return nil, ErrSecretUnavailable
		}
	}
	if counts != result.Summary || (exit == 0 && result.ChangeCount() != 0) || (exit == 1 && result.ChangeCount() == 0) {
		return nil, ErrSecretUnavailable
	}
	return &result, nil
}
func (s *SecretRunner) plan(ctx context.Context, id, stack, config string, draft bool) (*PlanResult, []byte, string, int, error) {
	if !s.valid() || !filepath.IsAbs(config) {
		return nil, nil, "", 0, ErrSecretUnavailable
	}
	if err := s.Runner.CheckSecretCapabilities(ctx, s.Engine); err != nil {
		return nil, nil, "", 0, err
	}
	binding, err := s.State.BindingPath(id, stack, s.Target, draft)
	if err != nil {
		return nil, nil, "", 0, ErrSecretUnavailable
	}
	key, err := s.State.ProofKeyPath(id)
	if err != nil {
		return nil, nil, "", 0, ErrSecretUnavailable
	}
	output, err := s.State.AllocateProofOutput(id)
	if err != nil {
		return nil, nil, "", 0, ErrSecretUnavailable
	}
	defer output.Close()
	data, exit, err := s.capture(ctx, "-f", config, "--engine-socket", s.Socket, "plan", "--format", "json", "--secret-bindings", binding, "--proof-key-file", key, "--proof-file", output.Path())
	if err != nil {
		return nil, nil, "", 0, err
	}
	plan, err := parseSecretPlan(data, exit)
	if err != nil {
		return nil, nil, "", 0, err
	}
	p, err := output.Read()
	if err != nil {
		return nil, nil, "", 0, ErrSecretUnavailable
	}
	proof, err := parseProof(p)
	if err != nil {
		return nil, nil, "", 0, err
	}
	return plan, data, proof, exit, nil
}

// Preview runs a fresh plan for the private draft and retains its proof privately.
func (s *SecretRunner) Preview(ctx context.Context, id, stack, config string) (*PlanResult, error) {
	if !s.valid() {
		return nil, ErrSecretUnavailable
	}
	unlock := s.Coordinator.Lock(id)
	defer unlock()
	plan, _, err := s.PreviewLocked(ctx, id, stack, config)
	return plan, err
}

// PreviewLocked requires the shared deployment coordinator lock.
func (s *SecretRunner) PreviewLocked(ctx context.Context, id, stack, config string) (*PlanResult, string, error) {
	if !s.valid() {
		return nil, "", ErrSecretUnavailable
	}
	candidate, err := s.State.Draft(id, stack, s.Target)
	if err != nil {
		return nil, "", ErrSecretUnavailable
	}
	plan, data, proof, exit, err := s.plan(ctx, id, stack, config, true)
	if err != nil {
		_ = s.State.InvalidatePreview(id)
		return nil, "", err
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		_ = s.State.InvalidatePreview(id)
		return nil, "", ErrSecretUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	if subtle.ConstantTimeCompare([]byte(token), []byte(proof)) == 1 {
		_ = s.State.InvalidatePreview(id)
		return nil, "", ErrSecretUnavailable
	}
	if err = s.State.SavePreview(id, stack, s.Target, candidate, proof, token, data, exit); err != nil {
		return nil, "", ErrSecretUnavailable
	}
	return plan, token, nil
}

// ApplyActive deploys only an already-approved active binding, never the draft.
func (s *SecretRunner) ApplyActive(ctx context.Context, id, stack, config string) error {
	if !s.valid() {
		return ErrSecretUnavailable
	}
	unlock := s.Coordinator.Lock(id)
	defer unlock()
	return s.applyActive(ctx, id, stack, config)
}
func (s *SecretRunner) applyActive(ctx context.Context, id, stack, config string) error {
	if !filepath.IsAbs(config) {
		return ErrSecretUnavailable
	}
	if err := s.Runner.CheckSecretCapabilities(ctx, s.Engine); err != nil {
		return err
	}
	binding, err := s.State.BindingPath(id, stack, s.Target, false)
	if err != nil {
		return ErrSecretUnavailable
	}
	_, exit, err := s.capture(ctx, "-f", config, "--engine-socket", s.Socket, "up", "--secret-bindings", binding)
	if err != nil || exit != 0 {
		return ErrSecretApplyFailed
	}
	return nil
}

// ConfirmAndApply repeats the candidate plan while locked, activates exactly
// the saved preview after constant-time proof comparison, then runs checked up.
func (s *SecretRunner) ConfirmAndApply(ctx context.Context, id, stack, config, token string) error {
	if !s.valid() {
		return ErrSecretUnavailable
	}
	unlock := s.Coordinator.Lock(id)
	defer unlock()
	return s.ConfirmAndApplyLocked(ctx, id, stack, config, token)
}

// ConfirmAndApplyLocked requires the shared deployment coordinator lock.
func (s *SecretRunner) ConfirmAndApplyLocked(ctx context.Context, id, stack, config, token string) error {
	if !s.valid() {
		return ErrSecretUnavailable
	}
	snapshot, err := s.State.Preview(id, stack, s.Target)
	if err != nil || subtle.ConstantTimeCompare([]byte(token), []byte(snapshot.Token)) != 1 {
		return secretbindings.ErrStale
	}
	_, data, proof, exit, err := s.plan(ctx, id, stack, config, true)
	if err != nil {
		_ = s.State.InvalidatePreview(id)
		return err
	}
	if err = s.State.ConfirmPreview(id, stack, s.Target, proof, token, data, exit); err != nil {
		if errors.Is(err, secretbindings.ErrStale) {
			return secretbindings.ErrStale
		}
		return ErrSecretApplyFailed
	}
	return s.applyActive(ctx, id, stack, config)
}

// CheckActive reports only point-in-time availability for an approved binding.
func (s *SecretRunner) CheckActive(ctx context.Context, id, stack, config string) error {
	return s.check(ctx, id, stack, config, false)
}

// CheckDraft checks the candidate without approving or activating it.
func (s *SecretRunner) CheckDraft(ctx context.Context, id, stack, config string) error {
	return s.check(ctx, id, stack, config, true)
}

func (s *SecretRunner) check(ctx context.Context, id, stack, config string, draft bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !s.valid() || !filepath.IsAbs(config) {
		return ErrSecretUnavailable
	}
	unlock := s.Coordinator.Lock(id)
	defer unlock()
	return s.CheckLocked(ctx, id, stack, config, draft)
}

// CheckLocked requires the shared deployment coordinator lock.
func (s *SecretRunner) CheckLocked(ctx context.Context, id, stack, config string, draft bool) error {
	if !s.valid() || !filepath.IsAbs(config) {
		return ErrSecretUnavailable
	}
	if err := s.Runner.CheckSecretCapabilities(ctx, s.Engine); err != nil {
		return err
	}
	binding, err := s.State.BindingPath(id, stack, s.Target, draft)
	if err != nil {
		return ErrSecretUnavailable
	}
	data, exit, err := s.capture(ctx, "-f", config, "--engine-socket", s.Socket, "secrets", "check", "--secret-bindings", binding, "--format", "json")
	if err != nil || exit != 0 {
		return ErrSecretUnavailable
	}
	m, err := strictObject(data, "contract", "status")
	if err != nil {
		return ErrSecretUnavailable
	}
	var contract, status string
	if json.Unmarshal(m["contract"], &contract) != nil || json.Unmarshal(m["status"], &status) != nil || contract != "dill.alpha-secrets/1" || status != "available" {
		return ErrSecretUnavailable
	}
	return nil
}
