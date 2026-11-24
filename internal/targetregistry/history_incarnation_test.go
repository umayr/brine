package targetregistry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func syntheticHistoryTarget(id string) Record {
	return Record{ID: id, Revision: 1, HTTPSAddress: "192.168.0.5:9443", HTTPSPairing: PairingSecret(fmt.Sprintf(
		`{"version":1,"certificateSha256":%q,"token":%q,"agentPublicKey":%q,"engineId":"11111111-1111-4111-8111-111111111111","generation":1,"engineKind":"docker","engineSocket":"unix:///run/docker.sock","daemonId":"synthetic"}`,
		strings.Repeat("11", 32), base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), strings.Repeat("33", 32)))}
}

func TestHistoryIncarnationPersistsAndDoesNotFollowForgottenID(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	dir := privateRegistryDir(t)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	r, err := OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	target := syntheticHistoryTarget("test-host")
	attempt, cancelAttempt := context.WithCancel(ctx)
	r.beforePublish = cancelAttempt
	if err := r.AddContext(attempt, target, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled enrollment publication acknowledged: %v", err)
	}
	r.beforePublish = nil
	before, err := os.ReadFile(filepath.Join(dir, "targets.json"))
	if err != nil || string(before) != emptyRegistry {
		t.Fatal("canceled enrollment partially published target or incarnation")
	}
	if err := r.AddContext(ctx, target, target); err != nil {
		t.Fatal(err)
	}
	get := func(r *Registry) HistoryIncarnation {
		t.Helper()
		got, id, lease, err := r.AcquireHistoryRead(ctx, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		if got != target || id == (HistoryIncarnation{}) {
			t.Fatal("acquired mismatched record or ID")
		}
		return id
	}
	first := get(r)
	// The previous registry reader disallowed unknown fields; intentional
	// fail-closed downgrade is preferable to losing an incarnation silently.
	diskBytes, err := os.ReadFile(filepath.Join(dir, "targets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Version int      `json:"version"`
		Targets []Record `json:"targets"`
	}
	decoder := json.NewDecoder(bytes.NewReader(diskBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err == nil {
		t.Fatal("downgrade accepted registry without recognizing history metadata")
	}
	if wire, err := json.Marshal(target); err != nil || bytes.Contains(wire, []byte("history_incarnation")) {
		t.Fatal("incarnation leaked into public record shape")
	}
	r, err = OpenExisting(dir)
	if err != nil || get(r) != first {
		t.Fatal("incarnation did not survive registry reopen")
	}
	_, _, lease, err := r.AcquireHistoryRead(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	err = r.ForgetHTTPS(blocked, target.ID)
	cancel()
	if closeErr := lease.Close(); !errors.Is(err, context.DeadlineExceeded) || closeErr != nil {
		t.Fatal("forget did not wait for the history lease")
	}
	if err := r.ForgetHTTPS(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := r.AcquireHistoryRead(ctx, target.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old enrollment available after forget")
	}
	if err := r.AddContext(ctx, target, target); err != nil {
		t.Fatal(err)
	}
	if get(r) == first {
		t.Fatal("replacement inherited a forgotten incarnation")
	}
}

func TestLegacyIncarnationAssignmentIsExplicitAndAtomic(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	dir := privateRegistryDir(t)
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	r, err := OpenExisting(dir)
	if err != nil {
		t.Fatal(err)
	}
	target := syntheticHistoryTarget("legacy")
	if err := r.lockedContext(ctx, func() error {
		return r.save(ctx, disk{Version: 2, Targets: []Record{target}})
	}); err != nil {
		t.Fatal(err)
	}
	prior, err := os.ReadFile(filepath.Join(dir, "targets.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, lease, err := r.AcquireRead(ctx, target.ID)
	if err != nil || got != target {
		t.Fatal("ordinary reader lost legacy target")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, lease, err := r.AcquireHistoryRead(ctx, target.ID); !errors.Is(err, ErrHistoryUnenrolled) || lease != nil {
		t.Fatal("legacy target was implicitly history-enabled or lease leaked")
	}
	if err := r.InitializeHistoryIncarnation(ctx, target, syntheticHistoryTarget("different")); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("unreviewed target initialized")
	}
	canceled, cancel := context.WithCancel(ctx)
	r.beforePublish = cancel
	if err := r.InitializeHistoryIncarnation(canceled, target, target); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled metadata publication acknowledged")
	}
	r.beforePublish = nil
	after, err := os.ReadFile(filepath.Join(dir, "targets.json"))
	if err != nil || !bytes.Equal(prior, after) {
		t.Fatal("canceled assignment changed the snapshot")
	}
	if err := r.InitializeHistoryIncarnation(ctx, target, target); err != nil {
		t.Fatal(err)
	}
	if err := r.InitializeHistoryIncarnation(ctx, target, target); !errors.Is(err, ErrConflict) {
		t.Fatal("initialization replaced a current incarnation")
	}
}

func TestHistoryIncarnationMetadataRejectsMalformedSnapshots(t *testing.T) {
	id := strings.Repeat("ab", 16)
	for name, invalid := range map[string]map[string]string{
		"unknown": {"missing": id}, "ssh": {record().ID: id}, "duplicate": {"a": id, "b": id},
		"empty": {"a": ""}, "noncanonical": {"a": strings.ToUpper(id)}, "zero": {"a": strings.Repeat("0", 32)},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Open(privateRegistryDir(t))
			if err != nil {
				t.Fatal(err)
			}
			d := disk{Version: 2, Targets: []Record{syntheticHistoryTarget("a"), syntheticHistoryTarget("b"), record()}, HistoryIncarnations: invalid}
			if err := r.save(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			if _, err := r.load(); err == nil {
				t.Fatal("malformed registry metadata accepted")
			}
		})
	}
	if err := rejectDuplicateKeys([]byte(fmt.Sprintf(`{"version":2,"targets":[],"history_incarnations":{"a":%q,"a":%q}}`, id, id))); err == nil {
		t.Fatal("duplicate incarnation key accepted")
	}
}
