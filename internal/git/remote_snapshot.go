package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/umayr/brine/internal/projectsnapshot"
)

var errRemoteSnapshot = errors.New("remote commit capture unavailable or unsupported")

// CaptureBranch snapshots the fetched branch tip directly from Git objects,
// without checkout filters, worktree changes or archive export attributes.
// Git transport, installed helpers and operator Git configuration are trusted.
func (r *Runner) CaptureBranch(ctx context.Context, remote, branch, entry string, limits projectsnapshot.Limits) (string, projectsnapshot.Snapshot, error) {
	var empty projectsnapshot.Snapshot
	if ctx == nil || ctx.Err() != nil || r == nil || remote == "" || strings.HasPrefix(remote, "-") || branch == "" || strings.HasPrefix(branch, "-") || len(branch) > 255 || limits.MaxFiles <= 0 || limits.MaxEntries <= 0 || limits.MaxEntries > 20000 || limits.MaxDepth <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxTotalBytes > 32<<20 {
		return "", empty, errRemoteSnapshot
	}
	ref := "refs/heads/" + branch
	if _, err := r.captureCommand(ctx, "", 1024, "check-ref-format", ref); err != nil {
		return "", empty, err
	}
	dir, err := os.MkdirTemp("", "brine-remote-git-")
	if err != nil {
		return "", empty, errRemoteSnapshot
	}
	defer os.RemoveAll(dir)
	if _, err = r.captureCommand(ctx, dir, 4096, "init", "--bare", "--quiet", "--template="); err != nil {
		return "", empty, err
	}
	if _, err = r.captureCommand(ctx, dir, 4096, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--depth=1", "--", remote, ref+":refs/brine/captured"); err != nil {
		return "", empty, err
	}
	raw, err := r.captureCommand(ctx, dir, 128, "rev-parse", "--verify", "refs/brine/captured^{commit}")
	if err != nil {
		return "", empty, err
	}
	commit := strings.TrimSuffix(string(raw), "\n")
	if !gitObjectID(commit) {
		return "", empty, errRemoteSnapshot
	}
	tree, err := r.captureCommand(ctx, dir, 8<<20, "ls-tree", "-rtz", "--full-tree", "--long", commit, "--")
	if err != nil {
		return "", empty, err
	}
	rows := bytes.Split(tree, []byte{0})
	if len(rows) < 2 || len(rows)-1 > limits.MaxEntries || len(rows[len(rows)-1]) != 0 {
		return "", empty, errRemoteSnapshot
	}
	snap := projectsnapshot.Snapshot{Entrypoint: entry}
	var total int64
	for _, row := range rows[:len(rows)-1] {
		meta, path, ok := strings.Cut(string(row), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || !gitObjectID(fields[2]) || !validGitSnapshotPath(path, limits.MaxDepth) {
			return "", empty, errRemoteSnapshot
		}
		if fields[0] == "040000" && fields[1] == "tree" && fields[3] == "-" {
			continue
		}
		if (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || len(snap.Files) >= limits.MaxFiles {
			return "", empty, errRemoteSnapshot
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 || size > limits.MaxFileBytes || size > limits.MaxTotalBytes-total {
			return "", empty, errRemoteSnapshot
		}
		content, err := r.captureCommand(ctx, dir, int(size), "cat-file", "blob", fields[2])
		if err != nil || int64(len(content)) != size {
			return "", empty, errRemoteSnapshot
		}
		total += size
		snap.Files = append(snap.Files, projectsnapshot.File{Path: path, Content: content, SHA256: sha256.Sum256(content)})
	}
	sort.Slice(snap.Files, func(i, j int) bool { return snap.Files[i].Path < snap.Files[j].Path })
	if projectsnapshot.ValidateProvisionalReceiver(ctx, snap) != nil {
		return "", empty, errRemoteSnapshot
	}
	return commit, snap, nil
}

func validGitSnapshotPath(name string, maxDepth int) bool {
	parts := strings.Split(name, "/")
	if len(parts) > maxDepth {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.EqualFold(p, ".git") || strings.Contains(p, "\\") {
			return false
		}
	}
	return true
}

func gitObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(b) == s
}

type captureOutput struct {
	buf   bytes.Buffer
	limit int
}

func (b *captureOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buf.Len() {
		return 0, errRemoteSnapshot
	}
	return b.buf.Write(p)
}

func (r *Runner) captureCommand(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	cmd := r.command(ctx, args...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = captureEnvironment(os.Environ())
	out := &captureOutput{limit: limit}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	if cmd.Run() != nil || ctx.Err() != nil {
		return nil, errRemoteSnapshot
	}
	return out.buf.Bytes(), nil
}

func captureEnvironment(source []string) []string {
	env := make([]string, 0, len(source)+1)
	for _, pair := range source {
		key, _, _ := strings.Cut(pair, "=")
		switch key {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_SHALLOW_FILE", "GIT_QUARANTINE_PATH", "GIT_REPLACE_REF_BASE", "GIT_TERMINAL_PROMPT", "GIT_GRAFT_FILE", "GIT_INTERNAL_SUPER_PREFIX", "GIT_PREFIX":
			continue
		}
		env = append(env, pair)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1")
}
