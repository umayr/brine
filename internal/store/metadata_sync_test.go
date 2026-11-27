package store

import (
	"errors"
	"testing"
)

func TestPredecessorSyncFailurePrecedesMetadataAcknowledgement(t *testing.T) {
	s, e := NewFSStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	s.SetMetadataObserver(func(MetadataChange) (MetadataCommit, error) {
		calls++
		return MetadataCommit{ID: "meta_pending", Done: func(error) {}}, nil
	})
	if _, e = s.Create("example", "original"); e != nil {
		t.Fatal(e)
	}
	unavailable := errors.New("directory sync unavailable")
	s.metadataSync = func(int) error { return unavailable }
	if _, e = s.Update("example", "must not replace source"); !errors.Is(e, unavailable) {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("undurable predecessor acknowledged", calls)
	}
	content, e := s.ReadPkl("example")
	if e != nil || content != "original" {
		t.Fatal("source changed before predecessor proof", e)
	}
}
