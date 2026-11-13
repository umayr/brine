package dill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/umayr/brine/internal/target"
)

// QualifiedOperationConfigured reports only explicit configuration presence. It
// does not validate the configuration, negotiate capabilities or attest readiness.
func (r *Runner) QualifiedOperationConfigured() bool { return r.QualifiedOperation != nil }
func ValidateQualifiedOperationConfig(engine, socket string, c *QualifiedOperationConfig) error {
	return validateQualifiedOperationConfig(runtime.GOOS, engine, socket, c)
}
func validateQualifiedOperationConfig(host, engine, socket string, c *QualifiedOperationConfig) error {
	if c == nil || host != "linux" || engine != "docker" || c.Layout != "native-local-docker-v1" || !filepath.IsAbs(c.StateDir) || filepath.Clean(c.StateDir) != c.StateDir || !validDaemonID(c.DaemonID) {
		return ErrQualifiedOperationConfig
	}
	if _, e := normalizeOperationSocket(socket); e != nil {
		return ErrQualifiedOperationConfig
	}
	fi, e := os.Lstat(c.StateDir)
	if e != nil || !fi.IsDir() || fi.Mode().Perm() != 0700 || !operationStateOwned(fi) {
		return ErrQualifiedOperationConfig
	}
	return nil
}
func validDaemonID(s string) bool {
	if len(s) < 8 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}
func operationDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func normalizeOperationSocket(value string) (string, error) {
	if !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) || strings.ContainsAny(value, "?#%\\") {
		return "", ErrOperationContract
	}
	path := value
	if !strings.HasPrefix(value, "/") {
		u, e := url.Parse(value)
		if e != nil || u.Scheme != "unix" || u.Host != "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || value != "unix://"+u.Path {
			return "", ErrOperationContract
		}
		path = u.Path
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.TrimRightFunc(path, unicode.IsSpace) != path {
		return "", ErrOperationContract
	}
	return "unix://" + path, nil
}

type operationOutput struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (o *operationOutput) Write(p []byte) (int, error) {
	if len(p) > o.max-o.buf.Len() {
		o.overflow = true
		return 0, ErrOperationContract
	}
	return o.buf.Write(p)
}
func (r *Runner) operationCapture(ctx context.Context, max int, args ...string) ([]byte, *int, error) {
	cmd, cleanup, e := r.command(ctx, args...)
	if e != nil {
		return nil, nil, ErrOperationContract
	}
	defer cleanup()
	out := operationOutput{max: max}
	diagnostics := operationOutput{max: 8192}
	cmd.Stdout = &out
	cmd.Stderr = &diagnostics
	e = cmd.Run()
	var exit *int
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		n := cmd.ProcessState.ExitCode()
		if n >= 0 {
			exit = &n
		}
	}
	if ctx.Err() != nil || out.overflow || diagnostics.overflow || exit == nil {
		return nil, exit, ErrOperationContract
	}
	if e != nil {
		var ee interface{ ExitCode() int }
		if !errors.As(e, &ee) {
			return nil, exit, ErrOperationContract
		}
	}
	return out.buf.Bytes(), exit, nil
}
func (r *Runner) checkOperationCapabilities(ctx context.Context, qualified bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, exit, e := r.operationCapture(ctx, 8192, "operation-capabilities", "--format", "json")
	if e != nil || exit == nil || *exit != 0 || !validOperationDiscovery(data, qualified) {
		return ErrOperationContract
	}
	return nil
}

func bindOperationReceipt(receipt *OperationReceipt, op, stack, engine, endpoint string, q *QualifiedOperationConfig) error {
	if receipt.Operation != op {
		return ErrOperationContract
	}
	if receipt.Target == nil {
		return nil
	} // Validated early rejection, never success.
	if receipt.Stack != stack || receipt.Target.Engine != engine || receipt.Target.EndpointDigest != operationDigest(endpoint) {
		return ErrOperationContract
	}
	identity := receipt.Target.QualifiedIdentityDigest
	if q == nil {
		if identity != "" {
			return ErrOperationContract
		}
	} else {
		expected := operationDigest("dill-local-docker-v1\x00" + q.DaemonID)
		if identity != "" && identity != expected {
			return ErrOperationContract
		}
		if (receipt.Outcome == "succeeded" || receipt.Effects != "none") && identity != expected {
			return ErrOperationContract
		}
	}
	return nil
}

// operation invokes only the pinned CLI adapter. No fallback, retry or replay is
// possible here. All output is captured privately and validated before emitting.
func (r *Runner) operation(ctx context.Context, path, op string, w io.Writer) (terminal OperationInvocation, err error) {
	terminal.Status = "unknown"
	terminal.ExitCode = -1
	defer func() {
		if w != nil {
			data, _ := json.Marshal(terminal)
			if _, e := io.WriteString(w, OperationMarker+string(data)+"\n"); e != nil && err == nil {
				err = &OperationError{Invocation: terminal}
			}
		}
		if err != nil {
			err = &OperationError{Invocation: terminal}
		}
	}()
	if !oneOf(op, "validate", "plan", "up", "start", "restart", "stop", "down") {
		return terminal, ErrOperationContract
	}
	qualified := !oneOf(op, "validate", "plan")
	q := r.QualifiedOperation
	if qualified || q != nil {
		if e := ValidateQualifiedOperationConfig(r.Engine, r.Socket, q); e != nil {
			return terminal, e
		}
	}
	if r.Profile == target.Alpha && q == nil {
		return terminal, target.ErrNegotiationUnavailable
	}
	endpoint, e := normalizeOperationSocket(r.Socket)
	if e != nil || !oneOf(r.Engine, "docker", "podman") || r.ResolveStack == nil {
		return terminal, ErrOperationContract
	}
	stack, e := r.ResolveStack(path)
	if e != nil || !operationStackPattern.MatchString(stack) {
		return terminal, ErrOperationContract
	}
	if e = r.checkSource(path); e != nil {
		return terminal, ErrOperationContract
	}
	// Preserve the existing known-secret deployment guard without invoking the
	// secret controller or legacy handler. Producer independently denies grants.
	guard := r.BeforeLegacyAction
	if op == "validate" {
		guard = r.BeforeLegacyValidate
	}
	if guard != nil && guard(ctx, path) != nil {
		return terminal, ErrOperationContract
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if e = r.checkOperationCapabilities(ctx, qualified); e != nil {
		return terminal, e
	}
	args := []string{"operation", op, "--contract", OperationVersion, "--format", "json", "--engine", r.Engine, "--engine-socket", endpoint, "-p", stack, "-f", path}
	if qualified {
		args = append(args, "--coordination-state-dir", q.StateDir, "--coordination-layout", q.Layout, "--coordination-daemon-id", q.DaemonID, "--coordination-source", "brine")
	}
	if op == "up" {
		args = append(args, "--no-prune", "--pull", "never")
	}
	data, exit, e := r.operationCapture(ctx, maxOperationOutput, args...)
	if exit != nil {
		terminal.ExitCode = *exit
	}
	if e != nil || exit == nil || !operationLine(data, maxOperationOutput) {
		return terminal, ErrOperationContract
	}
	receipt, e := parseOperationReceipt(data)
	if e != nil || bindOperationReceipt(receipt, op, stack, r.Engine, endpoint, func() *QualifiedOperationConfig {
		if qualified {
			return q
		}
		return nil
	}()) != nil {
		return terminal, ErrOperationContract
	}
	status := invocationStatus(receipt, *exit)
	// A process/receipt disagreement is not a validated terminal result.
	if status == "unknown" && receipt.Outcome != "unknown" {
		return terminal, ErrOperationContract
	}
	terminal.Receipt = receipt
	terminal.Status = status
	if status != "succeeded" && status != "changes" {
		return terminal, ErrOperationContract
	}
	return terminal, nil
}

func (r *Runner) operationPlan(ctx context.Context, path string) (*PlanResult, error) {
	terminal, e := r.operation(ctx, path, "plan", nil)
	if terminal.Receipt == nil {
		return nil, e
	}
	p := &PlanResult{Operation: terminal.Receipt}
	for _, x := range terminal.Receipt.Resources {
		if x.Kind != "service" {
			continue
		}
		switch x.Action {
		case "create":
			p.Summary.Create++
		case "recreate":
			p.Summary.Recreate++
		case "remove":
			p.Summary.Remove++
		case "noop":
			p.Summary.Noop++
		}
	}
	return p, e
}
