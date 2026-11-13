package dill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/umayr/brine/internal/secretbindings"
)

// Runner shells out to the dill binary for stack lifecycle operations.
// Stack configuration and parsing remain dill's responsibility.
type Runner struct {
	Binary       string // path to dill binary, e.g. "dill" or "/usr/local/bin/dill"
	PklBinary    string // explicit operator-configured Pkl binary, if any
	Profile      string // Alpha gates execution until Dill has a target/ownership contract
	Engine       string
	Socket       string
	ResolveStack func(string) (string, error)
	CheckSource  func(string) error

	// BeforeLegacyAction is configured at startup to prohibit downgrade of known secret deployments.
	BeforeLegacyAction   func(context.Context, string) error
	BeforeLegacyValidate func(context.Context, string) error
	StrictActions        StrictActions

	// Once set, every supported mutation uses the qualified CLI contract or fails.
	QualifiedOperation *QualifiedOperationConfig
	// Opt-in S1 requires a per-request durable observer for supported mutations.
	RequireOperationObserver bool

	Coordinator secretbindings.Coordinator

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// StrictActions routes all risky commands through the configured Alpha gate.
// Brine callers hold the shared deployment lock across each invocation.
type StrictActions interface {
	Up(context.Context, string) error
	Plan(context.Context, string) (*PlanResult, error)
	Start(context.Context, string, bool) error
	Validate(context.Context, string, *string) error
}

// LockStack coordinates Brine workspace and lifecycle operations for one stack.
func (r *Runner) LockStack(name string) func() {
	return r.Coordinator.Lock(secretbindings.DeploymentID(name))
}

// lockPath serialises lifecycle operations on the same stack so that a
// GitOps sync cannot run concurrently with a deploy triggered from the UI.
func (r *Runner) lockPath(path string) func() {
	r.mu.Lock()
	if r.locks == nil {
		r.locks = make(map[string]*sync.Mutex)
	}
	m := r.locks[path]
	if m == nil {
		m = &sync.Mutex{}
		r.locks[path] = m
	}
	r.mu.Unlock()
	m.Lock()
	return m.Unlock
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
	// Receipt contains opaque observations, not service names or field diffs.
	Operation *OperationReceipt `json:"operation,omitempty"`
	Changes   []PlanChange      `json:"changes"`
	Summary   PlanSummary       `json:"summary"`
}

// ChangeCount returns the number of services that would actually change.
// Noop services are excluded so the UI does not report "2 changes" for an
// unchanged stack.
func (p *PlanResult) ChangeCount() int {
	if p == nil {
		return 0
	}
	return p.Summary.Create + p.Summary.Recreate + p.Summary.Remove
}

// executable resolves bare binary names only against the fixed subprocess PATH,
// not the ambient Brine process PATH used by exec.LookPath.
func (r *Runner) executable() string {
	if strings.ContainsRune(r.Binary, filepath.Separator) {
		return r.Binary
	}
	for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		path := filepath.Join(dir, r.Binary)
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0111 != 0 {
			return path
		}
	}
	return filepath.Join("/usr/local/bin", r.Binary)
}

// command gives each invocation a private, empty home rather than exposing the
// server operator's credentials or Pkl configuration. The caller must clean it
// up after the subprocess exits, including on start failure or cancellation.
func (r *Runner) command(ctx context.Context, args ...string) (*exec.Cmd, func(), error) {
	home, err := os.MkdirTemp("", "brine-dill-home-")
	if err != nil {
		return nil, nil, fmt.Errorf("create private Dill runtime home: %w", err)
	}
	absoluteHome, err := filepath.Abs(home)
	if err != nil {
		_ = os.RemoveAll(home)
		return nil, nil, fmt.Errorf("resolve private Dill runtime home: %w", err)
	}
	home = absoluteHome
	cleanup := func() { _ = os.RemoveAll(home) }
	cmd := exec.CommandContext(ctx, r.executable(), args...)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C", "LC_ALL=C", "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache")}
	if r.PklBinary != "" {
		cmd.Env = append(cmd.Env, "DILL_PKL_PATH="+r.PklBinary)
	}
	return cmd, cleanup, nil
}

// run executes the dill binary with the given arguments, streaming combined
// stdout+stderr to w. Returns an error if the process exits non-zero.
func (r *Runner) run(ctx context.Context, w io.Writer, args ...string) error {
	cmd, cleanup, err := r.command(ctx, args...)
	if err != nil {
		return err
	}
	defer cleanup()
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
	cmd, cleanup, err := r.command(ctx, args...)
	if err != nil {
		return "", err
	}
	defer cleanup()
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

// lifecycleArgs binds Dill to Brine's trusted deployment identity and engine.
func (r *Runner) lifecycleArgs(path string, args ...string) ([]string, error) {
	if r.ResolveStack == nil || (r.Engine != "docker" && r.Engine != "podman") || !filepath.IsAbs(r.Socket) || filepath.Clean(r.Socket) != r.Socket {
		return nil, errors.New("lifecycle requires an explicit local engine endpoint")
	}
	name, err := r.ResolveStack(path)
	if err != nil || name == "" {
		return nil, errors.New("deployment identity unavailable")
	}
	return append([]string{"-f", path, "-p", name, "--engine", r.Engine, "--engine-socket", r.Socket}, args...), nil
}
func (r *Runner) runLifecycle(ctx context.Context, w io.Writer, path string, args ...string) error {
	argv, err := r.lifecycleArgs(path, args...)
	if err != nil {
		return err
	}
	if err = r.run(ctx, w, argv...); err != nil {
		return errors.New("Dill lifecycle failed; an endpoint-pinning compatible Dill may be required")
	}
	return nil
}

// checkSource rejects unsafe recipe paths before source-consuming commands.
func (r *Runner) checkSource(path string) error {
	if r.CheckSource != nil {
		return r.CheckSource(path)
	}
	return nil
}

// runLabelCleanup forces Dill's label-only fallback with a nonexistent config
// inside its private runtime home, never the workspace or process cwd.
func (r *Runner) runLabelCleanup(ctx context.Context, w io.Writer, path, action string) error {
	argv, err := r.lifecycleArgs(path, action)
	if err != nil {
		return err
	}
	cmd, cleanup, err := r.command(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	home := ""
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "HOME=") {
			home = strings.TrimPrefix(e, "HOME=")
			break
		}
	}
	if !filepath.IsAbs(home) {
		return errors.New("private cleanup context unavailable")
	}
	argv[1] = filepath.Join(home, "unavailable-config.pkl")
	cmd.Args = append([]string{cmd.Path}, argv...)
	cmd.Stdout = w
	cmd.Stderr = w
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("Dill label cleanup interrupted: %w", ctx.Err())
		}
		return errors.New("Dill lifecycle failed; an endpoint-pinning compatible Dill may be required")
	}
	return nil
}

// Up deploys a stack by running `dill -f <pklPath> up`, streaming output to w.
func (r *Runner) Up(ctx context.Context, pklPath string, w io.Writer) error {
	if r.QualifiedOperation != nil {
		defer r.lockPath(pklPath)()
		_, err := r.operation(ctx, pklPath, "up", w)
		return err
	}
	if err := r.checkExecutionContract(); err != nil {
		return err
	}
	defer r.lockPath(pklPath)()
	if err := r.checkSource(pklPath); err != nil {
		return err
	}
	if r.StrictActions != nil {
		return r.StrictActions.Up(ctx, pklPath)
	}
	if r.BeforeLegacyAction != nil {
		if err := r.BeforeLegacyAction(ctx, pklPath); err != nil {
			return err
		}
	}
	return r.runLifecycle(ctx, w, pklPath, "up")
}

// Down stops and removes all containers in the stack.
func (r *Runner) Down(ctx context.Context, pklPath string, w io.Writer) error {
	if r.QualifiedOperation != nil {
		defer r.lockPath(pklPath)()
		_, err := r.operation(ctx, pklPath, "down", w)
		return err
	}
	defer r.lockPath(pklPath)()
	return r.runLabelCleanup(ctx, w, pklPath, "down")
}

// Stop stops running containers without removing them.
func (r *Runner) Stop(ctx context.Context, pklPath string, w io.Writer) error {
	if r.QualifiedOperation != nil {
		defer r.lockPath(pklPath)()
		_, err := r.operation(ctx, pklPath, "stop", w)
		return err
	}
	defer r.lockPath(pklPath)()
	return r.runLabelCleanup(ctx, w, pklPath, "stop")
}

// Start starts previously stopped containers.
func (r *Runner) Start(ctx context.Context, pklPath string, w io.Writer) error {
	if r.QualifiedOperation != nil {
		defer r.lockPath(pklPath)()
		_, err := r.operation(ctx, pklPath, "start", w)
		return err
	}
	if err := r.checkExecutionContract(); err != nil {
		return err
	}
	defer r.lockPath(pklPath)()
	if err := r.checkSource(pklPath); err != nil {
		return err
	}
	if r.StrictActions != nil {
		return r.StrictActions.Start(ctx, pklPath, false)
	}
	if r.BeforeLegacyAction != nil {
		if err := r.BeforeLegacyAction(ctx, pklPath); err != nil {
			return err
		}
	}
	return r.runLifecycle(ctx, w, pklPath, "start")
}

// Restart stops then starts all containers.
func (r *Runner) Restart(ctx context.Context, pklPath string, w io.Writer) error {
	if r.QualifiedOperation != nil {
		defer r.lockPath(pklPath)()
		_, err := r.operation(ctx, pklPath, "restart", w)
		return err
	}
	if err := r.checkExecutionContract(); err != nil {
		return err
	}
	defer r.lockPath(pklPath)()
	if err := r.checkSource(pklPath); err != nil {
		return err
	}
	if r.StrictActions != nil {
		return r.StrictActions.Start(ctx, pklPath, true)
	}
	if r.BeforeLegacyAction != nil {
		if err := r.BeforeLegacyAction(ctx, pklPath); err != nil {
			return err
		}
	}
	return r.runLifecycle(ctx, w, pklPath, "restart")
}

// Teardown removes containers, images, volumes, and the stack network.
// Pass force=true to force-remove images referenced by other containers.
func (r *Runner) Teardown(ctx context.Context, pklPath string, force bool, w io.Writer) error {
	if r.QualifiedOperation != nil {
		return ErrOperationContract
	}
	if err := r.checkExecutionContract(); err != nil {
		return err
	}
	defer r.lockPath(pklPath)()
	if err := r.checkSource(pklPath); err != nil {
		return errors.New("teardown requires a safe recipe; stop and down remain available")
	}
	args := []string{"teardown"}
	if force {
		args = append(args, "--force")
	}
	return r.runLifecycle(ctx, w, pklPath, args...)
}

// PS lists containers in the stack using `dill ps --format json`.
// Returns an empty slice if the stack is not deployed.
func (r *Runner) PS(ctx context.Context, pklPath string) ([]PSContainer, error) {
	if err := r.checkSource(pklPath); err != nil {
		return nil, err
	}
	args, err := r.lifecycleArgs(pklPath, "ps", "--format", "json")
	if err != nil {
		return nil, err
	}
	out, err := r.capture(ctx, args...)
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
	if r.QualifiedOperation != nil {
		return r.operationPlan(ctx, pklPath)
	}
	if err := r.checkExecutionContract(); err != nil {
		return nil, err
	}
	if err := r.checkSource(pklPath); err != nil {
		return nil, err
	}
	if r.StrictActions != nil {
		return r.StrictActions.Plan(ctx, pklPath)
	}
	if r.BeforeLegacyAction != nil {
		if err := r.BeforeLegacyAction(ctx, pklPath); err != nil {
			return nil, err
		}
	}
	args, err := r.lifecycleArgs(pklPath, "plan", "--format", "json")
	if err != nil {
		return nil, err
	}
	out, err := r.capture(ctx, args...)
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
	if r.QualifiedOperation != nil || r.Profile == "alpha" {
		_, err := r.operation(ctx, pklPath, "validate", nil)
		return err
	}
	if err := r.checkSource(pklPath); err != nil {
		return err
	}
	if r.StrictActions != nil {
		return r.StrictActions.Validate(ctx, pklPath, nil)
	}
	if r.BeforeLegacyValidate != nil {
		if err := r.BeforeLegacyValidate(ctx, pklPath); err != nil {
			return err
		}
	}
	args, err := r.lifecycleArgs(pklPath, "validate", "--format", "json")
	if err != nil {
		return err
	}
	out, err := r.capture(ctx, args...)
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
	if r.QualifiedOperation != nil || r.Profile == "alpha" {
		return ErrOperationContract
	}
	if err := r.checkSource(pklPath); err != nil {
		return err
	}
	if r.StrictActions != nil {
		return r.StrictActions.Validate(ctx, pklPath, &content)
	}
	if r.BeforeLegacyValidate != nil {
		if err := r.BeforeLegacyValidate(ctx, pklPath); err != nil {
			return err
		}
	}
	var stdout, stderr bytes.Buffer
	args, err := r.lifecycleArgs(pklPath, "validate", "--stdin", "--format", "json")
	if err != nil {
		return err
	}
	cmd, cleanup, err := r.command(ctx, args...)
	if err != nil {
		return err
	}
	defer cleanup()
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
	if err := r.checkSource(pklPath); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd, cleanup, err := r.command(ctx, "-f", pklPath, "fmt", "--stdin")
	if err != nil {
		return "", err
	}
	defer cleanup()
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
