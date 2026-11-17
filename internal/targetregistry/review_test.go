package targetregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCanonicalKeys(t *testing.T) {
	b, err := json.MarshalIndent(disk{Version: 2, Targets: []Record{record()}}, "", "  ")
	b = append(b, '\n')
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{`"version": 2`, `"version": 2,"VERSION":2`}, {`"version": 2`, `"VERSION": 2`}, {`"host_key_sha256":`, `"HOST_KEY_SHA256":`}, {`"host_key_sha256":`, `"HOST_KEY_SHA256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","host_key_sha256":`}, {`"host_public_key":`, `"Host_Public_Key":`}} {
		t.Run(pair[1], func(t *testing.T) {
			r := registry(t)
			s := strings.Replace(string(b), pair[0], pair[1], 1)
			if s == string(b) {
				t.Fatal("test substitution failed")
			}
			if err := os.WriteFile(filepath.Join(r.dir, "targets.json"), []byte(s), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := r.List(); err == nil {
				t.Fatal("accepted noncanonical fields")
			}
			if err := r.Add(record(), record()); err == nil {
				t.Fatal("overwrote corrupt file")
			}
		})
	}
}
func TestFIFOStorageDoesNotBlock(t *testing.T) {
	r := registry(t)
	if err := unix.Mkfifo(filepath.Join(r.dir, "targets.json"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := r.List(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted FIFO")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO open blocked")
	}
}
func TestPublicKeyPinValidation(t *testing.T) {
	r := record()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Record){func(r *Record) { r.HostKeySHA256 = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }, func(r *Record) { r.HostPublicKey += " comment" }, func(r *Record) { r.HostPublicKey += "\n" }, func(r *Record) { r.HostPublicKey = "not-a-key" }, func(r *Record) { r.HostPublicKey = "" }} {
		bad := r
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatalf("accepted bad pin %+v", bad)
		}
	}
}
