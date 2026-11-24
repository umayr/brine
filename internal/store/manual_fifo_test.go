package store

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManualLedgerFIFORejectedWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	s, _ := NewFSStore(root)
	if _, err := s.ListManualIntents(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, manualIntentFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.ListManualIntents(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		// Unblock a regressed blocking open so the test does not leak its worker.
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if err == nil {
			unix.Close(fd)
		}
		t.Fatal("FIFO blocked ledger open")
	}
}
