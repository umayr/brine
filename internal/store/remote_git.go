package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// RemoteGitPolicy lives with the existing stack metadata. Request is written
// durably before the first submission. An unresolved request is never cleared
// by disabling automation, restarting Brine, or re-pairing the target.
type RemoteGitPolicy struct {
	SourceSync        string `json:"source_sync,omitempty"`
	DeploymentRequest string `json:"deployment_request,omitempty"`
	DeploymentJournal string `json:"deployment_journal,omitempty"`
	Enabled           bool   `json:"enabled"`
	Host              string `json:"host"`
	Target            string `json:"target"`
	Source            string `json:"source"`
	Commit            string `json:"commit,omitempty"`
	Seen              string `json:"seen,omitempty"`
	Request           string `json:"request,omitempty"`
	Phase             string `json:"phase"`
	Journal           string `json:"journal,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

type RemoteGitStore interface {
	SwapRemoteGit(name, source string, expected, replacement *RemoteGitPolicy) error
}

// A policy stores only the exact attempted object IDs, not snapshots or results.
// Bound metadata growth by bytes; never evict an ID to make room for another.
const remoteGitSeenBytes = 1 << 20

func (p *RemoteGitPolicy) CanRecordAttempt() bool {
	return len(p.Seen)+65 <= remoteGitSeenBytes // separator + longest Git object ID
}

func (p *RemoteGitPolicy) AttemptHistoryLow() bool {
	return len(p.Seen) >= remoteGitSeenBytes*9/10
}

var ErrRemoteGit = errors.New("remote Git configuration changed or is unavailable")

func (s *Stack) RemoteGitSource() string {
	if s == nil || s.Git == nil || s.Git.URL == "" || s.Git.Folder != "" || s.Git.AutoDeploy {
		return ""
	}
	b, _ := json.Marshal([]string{s.Name, s.Git.URL, s.RemoteGitBranch(), entrypointOrDefault(s.Entrypoint)})
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

func (s *Stack) RemoteGitBranch() string {
	if s.Git.Branch == "" {
		return "main"
	}
	return s.Git.Branch
}

func (s *Stack) protectsRemoteGit() bool {
	return s.RemoteGit != nil && (s.RemoteGit.Enabled || s.RemoteGit.Request != "")
}

func remoteGitHex(s string, n int) bool {
	if len(s) != 2*n {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(b) == s
}
func (p *RemoteGitPolicy) valid() bool {
	if p == nil {
		return true
	}
	if (p.DeploymentRequest != "" || p.DeploymentJournal != "") && (!remoteGitHex(p.DeploymentRequest, 16) || !remoteGitHex(p.DeploymentJournal, 16)) {
		return false
	}
	if (p.SourceSync != "" && (!remoteGitHex(p.SourceSync, 16) || p.Phase != "blocked" || p.Reason != "source-sync-unconfirmed")) || (p.Reason == "source-sync-unconfirmed" && !remoteGitHex(p.SourceSync, 16)) {
		return false
	}
	if p.Phase == "disabled" && p.Enabled {
		return false
	}
	if p.Host == "" || len(p.Host) > 128 || !remoteGitHex(p.Target, 32) || !remoteGitHex(p.Source, 32) {
		return false
	}
	if p.Commit != "" && !remoteGitHex(p.Commit, 20) && !remoteGitHex(p.Commit, 32) {
		return false
	}
	if len(p.Seen) > remoteGitSeenBytes {
		return false
	}
	seen := strings.Fields(p.Seen)
	if strings.Join(seen, " ") != p.Seen || (len(seen) > 0 && seen[len(seen)-1] != p.Commit) || (len(seen) == 0 && p.Commit != "") {
		return false
	}
	unique := make(map[string]bool, len(seen))
	for _, id := range seen {
		if unique[id] || (!remoteGitHex(id, 20) && !remoteGitHex(id, 32)) {
			return false
		}
		unique[id] = true
	}
	if p.Request != "" && (!remoteGitHex(p.Request, 16) || p.Commit == "") {
		return false
	}
	if p.Journal != "" && (p.Request == "" || !remoteGitHex(p.Journal, 16)) {
		return false
	}
	switch p.Phase {
	case "ready", "succeeded", "disabled":
		return p.Request == "" && p.Reason == ""
	case "blocked":
		return p.Request == "" && (p.Reason == "" || p.Reason == "staging-or-preview" || p.Reason == "source-sync-unconfirmed" || p.Reason == "no-journal" || p.Reason == "journal-capacity" || p.Reason == "not-admitted" || p.Reason == "failed")
	case "pending":
		return p.Request != "" && (p.Reason == "" || p.Reason == "awaiting-status" || p.Reason == "running" || p.Reason == "unknown" || p.Reason == "status-unavailable")
	default:
		return false
	}
}

func (s *FSStore) SwapRemoteGit(name, source string, expected, replacement *RemoteGitPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stack, err := s.readMeta(name)
	if err != nil || !replacement.valid() || source == "" || stack.RemoteGitSource() != source || (stack.RemoteGit == nil) != (expected == nil) || (expected != nil && *stack.RemoteGit != *expected) {
		return ErrRemoteGit
	}
	if replacement == nil || replacement.Source != source {
		return ErrRemoteGit
	}
	if expected != nil && expected.Source == replacement.Source && expected.Target == replacement.Target && expected.Seen != replacement.Seen && expected.Seen != "" && !strings.HasPrefix(replacement.Seen, expected.Seen+" ") {
		return ErrRemoteGit
	}
	stack.RemoteGit = replacement
	return s.writeMeta(stack)
}
