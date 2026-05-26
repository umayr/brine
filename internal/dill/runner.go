package dill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Runner shells out to the dill binary for stack lifecycle operations.
// Stack configuration and parsing remain dill's responsibility.
type Runner struct {
	Binary string // path to dill binary, e.g. "dill" or "/usr/local/bin/dill"
}

// New returns a Runner using the given binary path.
func New(binary string) *Runner {
	return &Runner{Binary: binary}
}

// PSContainer represents a container reported by `dill ps --format json`.
type PSContainer struct {
	Name   string   `json:"name"`
	ID     string   `json:"id"`
	State  string   `json:"state"`
	Status string   `json:"status"`
	Image  string   `json:"image"`
	Ports  []string `json:"ports"`
}

// FieldDiff is a single field-level difference within a plan change.
type FieldDiff struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// PlanChange is a single service change from `dill plan --format json`.
type PlanChange struct {
	Service string      `json:"service"`
	Kind    string      `json:"kind"` // create | recreate | remove | noop
	Diffs   []FieldDiff `json:"diffs"`
}

// PlanSummary is the per-action count breakdown in a plan result.
type PlanSummary struct {
	Create   int `json:"create"`
	Recreate int `json:"recreate"`
	Remove   int `json:"remove"`
	Noop     int `json:"noop"`
}

// PlanResult is the structured output of `dill plan --format json`.
type PlanResult struct {
	Changes []PlanChange `json:"changes"`
	Summary PlanSummary  `json:"summary"`
}

// run executes the dill binary with the given arguments, streaming combined
// stdout+stderr to w. Returns an error if the process exits non-zero.
func (r *Runner) run(ctx context.Context, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dill %v: %w", args, err)
	}
	return nil
}

// capture runs dill and returns combined output as a string.
func (r *Runner) capture(ctx context.Context, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// Up deploys a stack by running `dill -f <pklPath> up`, streaming output to w.
func (r *Runner) Up(ctx context.Context, pklPath string, w io.Writer) error {
	return r.run(ctx, w, "-f", pklPath, "up")
}

// Down stops and removes all containers in the stack.
func (r *Runner) Down(ctx context.Context, pklPath string, w io.Writer) error {
	return r.run(ctx, w, "-f", pklPath, "down")
}

// Stop stops running containers without removing them.
func (r *Runner) Stop(ctx context.Context, pklPath string, w io.Writer) error {
	return r.run(ctx, w, "-f", pklPath, "stop")
}

// Start starts previously stopped containers.
func (r *Runner) Start(ctx context.Context, pklPath string, w io.Writer) error {
	return r.run(ctx, w, "-f", pklPath, "start")
}

// Restart stops then starts all containers.
func (r *Runner) Restart(ctx context.Context, pklPath string, w io.Writer) error {
	return r.run(ctx, w, "-f", pklPath, "restart")
}

// Teardown removes containers, images, volumes, and the stack network.
// Pass force=true to force-remove images referenced by other containers.
func (r *Runner) Teardown(ctx context.Context, pklPath string, force bool, w io.Writer) error {
	args := []string{"-f", pklPath, "teardown"}
	if force {
		args = append(args, "--force")
	}
	return r.run(ctx, w, args...)
}

// PS lists containers in the stack using `dill ps --format json`.
// Returns an empty slice if the stack is not deployed.
func (r *Runner) PS(ctx context.Context, pklPath string) ([]PSContainer, error) {
	out, err := r.capture(ctx, "-f", pklPath, "ps", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("dill ps: %w", err)
	}
	var containers []PSContainer
	if err := json.Unmarshal([]byte(out), &containers); err != nil {
		return nil, fmt.Errorf("dill ps: invalid JSON: %w", err)
	}
	return containers, nil
}

// Plan returns a structured diff of what `dill up` would change.
// Exit 0 = no changes, exit 1 = has changes (not an error), exit 2 = error.
func (r *Runner) Plan(ctx context.Context, pklPath string) (*PlanResult, error) {
	out, err := r.capture(ctx, "-f", pklPath, "plan", "--format", "json")
	if err != nil && !isExitCode(err, 1) {
		if out != "" {
			return nil, fmt.Errorf("%s", strings.TrimSpace(out))
		}
		return nil, fmt.Errorf("dill plan: %w", err)
	}
	var result PlanResult
	if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
		return nil, fmt.Errorf("dill plan: invalid JSON: %w", jsonErr)
	}
	return &result, nil
}

// Validate checks the .pkl file for errors using `dill validate --format json`.
// Returns nil if the config is valid, or an error with the dill-reported message.
// NOTE: dill exits 0 in both valid and invalid cases when --format json is used;
// validity must be determined by parsing the JSON output, not the exit code.
func (r *Runner) Validate(ctx context.Context, pklPath string) error {
	out, err := r.capture(ctx, "-f", pklPath, "validate", "--format", "json")
	if err != nil {
		// Unexpected non-zero exit (e.g. file not found). Fall back to raw output.
		if msg := strings.TrimSpace(out); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return parseValidateJSON(out)
}

// ValidateContent validates in-memory content using `dill validate --stdin`.
// pklPath is used for directory context (PKL import resolution).
func (r *Runner) ValidateContent(ctx context.Context, pklPath, content string) error {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.Binary, "-f", pklPath, "validate", "--stdin", "--format", "json")
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Unexpected non-zero exit (e.g. binary not found). Fall back to raw output.
		out := stdout.String()
		if errOut := stderr.String(); errOut != "" {
			out = errOut
		}
		if msg := strings.TrimSpace(out); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	// dill exits 0 in both valid and invalid cases with --format json;
	// parse the JSON body to determine actual validity.
	return parseValidateJSON(stdout.String())
}

// Fmt formats content using `dill fmt --stdin`, returning the formatted output.
// The pklPath is used for context (directory resolution of PKL imports).
func (r *Runner) Fmt(ctx context.Context, pklPath, content string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.Binary, "-f", pklPath, "fmt", "--stdin")
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s", msg)
		}
		if msg := strings.TrimSpace(stdout.String()); msg != "" {
			return "", fmt.Errorf("%s", msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

// parseValidateJSON parses the JSON output of `dill validate --format json`.
// Returns nil if valid, or an error with the dill-reported message if invalid.
func parseValidateJSON(out string) error {
	var resp struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		return fmt.Errorf("dill validate: invalid JSON: %w", err)
	}
	if !resp.Valid {
		if resp.Error != "" {
			return fmt.Errorf("%s", resp.Error)
		}
		return fmt.Errorf("invalid configuration")
	}
	return nil
}

// isExitCode reports whether err is an exit with the given code.
func isExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode() == code
	}
	return false
}
