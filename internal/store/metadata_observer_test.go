package store

import "testing"

func TestNoopMetadataWritePreservesCommittedEventHead(t *testing.T) {
	s, e := NewFSStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	calls := 0
	s.SetMetadataObserver(func(MetadataChange) (MetadataCommit, error) {
		calls++
		return MetadataCommit{ID: "meta_committed", Done: func(error) {}}, nil
	})
	if _, e = s.Create("example", "// fixture\n"); e != nil {
		t.Fatal(e)
	}
	st, e := s.Get("example")
	if e != nil {
		t.Fatal(e)
	}
	st.EventHead = ""
	s.mu.Lock()
	e = s.writeMeta(st)
	s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Get("example")
	if e != nil || got.EventHead != "meta_committed" || calls != 1 {
		t.Fatal("no-op metadata write rewound commit evidence", got, e, calls)
	}
}
