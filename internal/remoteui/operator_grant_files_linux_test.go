//go:build linux

package remoteui

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/agentclient"
	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/sys/unix"
)

func TestPrivateOperatorGrantSourceThroughAdapter(t *testing.T) {
	exerciseOperatorAuthority(t, func(t *testing.T, approved operatorGrantApproval, token string) (operatorGrantSource, func()) {
		root := realAttendedArchiveHome(t)
		name := filepath.Join(root, approved.Reference)
		if err := os.WriteFile(name, []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		source, err := newPrivateOperatorGrantSource(root, approved)
		if err != nil {
			t.Fatal(err)
		}
		return source, func() {
			substitute := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
			pending := filepath.Join(root, "replacement")
			if err := os.WriteFile(pending, []byte(substitute), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(pending, name); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestPrivateOperatorGrantSourceRejectsUnsafeAndUnapprovedFiles(t *testing.T) {
	ctx := context.Background()
	root := realAttendedArchiveHome(t)
	token := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("q", 32)))
	approval := operatorGrantApproval{Enrollment: targetregistry.HistoryIncarnation{1}, Reference: strings.Repeat("a", 64), Revision: strings.Repeat("b", 64), LocalGrants: agentclient.LocalNewAttended, TokenCommitment: sha256.Sum256([]byte(token))}
	assertDenied := func(t *testing.T, s *privateOperatorGrantSource, active targetregistry.HistoryIncarnation, ctx context.Context) {
		t.Helper()
		got, bearer, err := s.LoadCurrent(ctx, active)
		if err != errOperatorAuthority || bearer != "" || got != (operatorGrantApproval{}) || strings.Contains(err.Error(), root) || strings.Contains(err.Error(), token) {
			t.Fatal("denial returned sensitive data or an approval")
		}
	}
	wrapper, err := newPrivateOperatorGrantSource(root, approval)
	if err != nil {
		t.Fatal(err)
	}
	if rendered := fmt.Sprintf("%v %+v %#v", wrapper, wrapper, wrapper); strings.Contains(rendered, root) || strings.Contains(rendered, approval.Reference) {
		t.Fatal("formatted source leaked private fields")
	}
	if data, err := json.Marshal(wrapper); err == nil || len(data) != 0 {
		t.Fatal("private source is serializable")
	}
	invalid := approval
	invalid.Reference = "../outside"
	if s, err := newPrivateOperatorGrantSource(root, invalid); s != nil || err != errOperatorAuthority {
		t.Fatal("unreviewed path-shaped reference accepted")
	}
	for _, tc := range []struct {
		name   string
		mutate func(string, string) error
	}{
		{"group-readable", func(_, p string) error { return os.Chmod(p, 0640) }},
		{"writable-root", func(dir, _ string) error { return os.Chmod(dir, 0770) }},
		{"symlink", func(dir, p string) error {
			target := filepath.Join(dir, "outside")
			if err := os.Rename(p, target); err != nil {
				return err
			}
			return os.Symlink(target, p)
		}},
		{"hardlink", func(dir, p string) error { return os.Link(p, filepath.Join(dir, "second")) }},
		{"fifo", func(_, p string) error {
			if err := os.Remove(p); err != nil {
				return err
			}
			return unix.Mkfifo(p, 0600)
		}},
		{"oversized", func(_, p string) error { return os.WriteFile(p, []byte(token+"\n\n"), 0600) }},
		{"malformed", func(_, p string) error { return os.WriteFile(p, []byte(strings.Repeat("?", 43)), 0600) }},
		{"different canonical bytes", func(_, p string) error { return os.WriteFile(p, []byte(strings.Repeat("A", 43)), 0600) }},
		{"missing", func(_, p string) error { return os.Remove(p) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp(root, "grant-")
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, approval.Reference)
			if err := os.WriteFile(p, []byte(token), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := newPrivateOperatorGrantSource(dir, approval)
			if err != nil {
				t.Fatal(err)
			}
			if got, bearer, err := s.LoadCurrent(ctx, approval.Enrollment); err != nil || bearer != token || got != approval {
				t.Fatal("fixture initial approval was not readable")
			}
			if err := tc.mutate(dir, p); err != nil {
				t.Fatal(err)
			}
			assertDenied(t, s, approval.Enrollment, ctx)
		})
	}
	// A missing root cannot be used to test wrong enrollment: give both paths
	// valid files and verify the deny is caused by the enrollment/cancel check.
	if err := os.WriteFile(filepath.Join(root, approval.Reference), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	assertDenied(t, wrapper, targetregistry.HistoryIncarnation{2}, ctx)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assertDenied(t, wrapper, approval.Enrollment, cancelled)
	linked := filepath.Join(root, "linked-dir")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	s, err := newPrivateOperatorGrantSource(linked, approval)
	if err != nil {
		t.Fatal(err)
	}
	assertDenied(t, s, approval.Enrollment, ctx)
}
