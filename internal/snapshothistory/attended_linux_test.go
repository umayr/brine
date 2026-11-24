//go:build linux

package snapshothistory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAttendedBindingAuthenticatesAcrossArchiveReopen(t *testing.T) {
	ctx := context.Background()
	o, _ := fixture(t, 1<<20, 10)
	s, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := request(71)
	r.Intent.Attended = &AttendedIntent{
		Target: strings.Repeat("a", 64), AuthorityRevision: strings.Repeat("b", 64),
		Stack: "synthetic", Mode: "update", Stage: strings.Repeat("c", 32), Selection: "sha256:" + strings.Repeat("d", 64),
	}
	want := *r.Intent.Attended
	archived, err := s.Commit(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.Intent.Attended.Stage = strings.Repeat("f", 32)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Open(ctx, archived)
	if err != nil || got.Intent.Attended == nil || *got.Intent.Attended != want {
		t.Fatalf("attended intent not preserved: %v", err)
	}
	for _, wrong := range []*AttendedIntent{nil, r.Intent.Attended} {
		mismatch := archived
		mismatch.Intent.Attended = wrong
		if _, err := reopened.Open(ctx, mismatch); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unbound or wrong attended context accepted: %v", err)
		}
	}
}
