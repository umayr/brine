package webhooks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExistingStorageNeverSilentlyReinitializes(t *testing.T) {
	cfg := Config{Directory: filepath.Join(t.TempDir(), "state"), EncryptionKey: bytes.Repeat([]byte{1}, 32)}
	if s, e := OpenExisting(cfg); e == nil {
		s.Close()
		t.Fatal("uninitialized state opened")
	}
	if e := Initialize(cfg); e != nil {
		t.Fatal(e)
	}
	if e := Initialize(cfg); e == nil {
		t.Fatal("initialization overwrote existing state")
	}
	s, e := OpenExisting(cfg)
	if e != nil {
		t.Fatal(e)
	}
	testEndpoint(t, s)
	s.Close()
	os.Remove(filepath.Join(cfg.Directory, "webhooks.db"))
	os.Remove(filepath.Join(cfg.Directory, "webhooks.db-wal"))
	os.Remove(filepath.Join(cfg.Directory, "webhooks.db-shm"))
	if reopened, e := Open(cfg); e == nil {
		reopened.Close()
		t.Fatal("missing database silently reinitialized")
	}
	if reopened, e := OpenExisting(cfg); e == nil {
		reopened.Close()
		t.Fatal("lost state presented as empty")
	}
}
func TestIncomingReceiptPreservesImmutableBinding(t *testing.T) {
	s := testService(t, 10)
	tr, key := testTrigger(t, s)
	r, e := acceptRequest(s, tr, key, `{"id":"immutable-binding"}`, &testDispatcher{})
	if e != nil {
		t.Fatal(e)
	}
	if r.Binding.StackID != tr.StackID || r.Binding.TargetID != tr.TargetID || r.Binding.Action != tr.Action {
		t.Fatal("binding absent from receipt")
	}
	s.DeleteTrigger(context.Background(), tr.ID, tr.Version)
	records, _ := s.Requests(context.Background(), tr.ID)
	if records[0].Binding.StackID != tr.StackID || records[0].Binding.TargetID != tr.TargetID {
		t.Fatal("retired binding lost")
	}
}
