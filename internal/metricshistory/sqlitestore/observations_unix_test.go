//go:build linux || darwin

package sqlitestore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/umayr/brine/internal/agentmetrics"
)

func TestObservationStorePreservesPresenceAndEnrollment(t *testing.T) {
	dir := persistentFixture(t)
	ctx := context.Background()
	id, replacement := EnrollmentID{1}, EnrollmentID{2}
	now := time.Unix(1700000000, 123).UTC()
	measuredZero := Observation{Received: now, Sample: agentmetrics.Sample{HasOwnedCount: true, OwnedCount: 1, HasOwnedUsage: true}}
	absentUsage := Observation{Received: now.Add(time.Nanosecond), Sample: agentmetrics.Sample{HasHost: true, HostTotal: 1024, HasOwnedCount: true}}
	if got, err := OpenObservations(dir); err == nil {
		_ = got.Close()
		t.Fatal("ordinary startup implicitly initialized a store")
	}
	path := filepath.Join(dir, HistoryFilename)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("startup changed unprovisioned directory: %v", err)
	}
	if err := InitializeObservations(dir); err != nil {
		t.Fatal(err)
	}
	s, err := OpenObservations(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(ctx, id, measuredZero); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, id, absentUsage); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, id, Observation{Received: now, Sample: absentUsage.Sample}); err == nil {
		t.Fatal("duplicate receipt replaced a capture")
	}
	for _, invalid := range []agentmetrics.Sample{
		{}, {HasOwnedCount: true, OwnedCount: 2}, {HasOwnedUsage: true},
		{HasHost: true, HostTotal: 1024, HostAvailable: 1},
		{HasHost: true, HostTotal: 1024, OwnedCount: 2},
		{HasOwnedCount: true, OwnedCount: 1, HasOwnedUsage: true, OwnedUsage: 1 << 53},
	} {
		if err := s.Append(ctx, id, Observation{Received: now.Add(time.Second), Sample: invalid}); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenObservations(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Read(ctx, replacement, now, now.Add(time.Second), 20)
	if err != nil || len(got) != 0 {
		t.Fatal("replacement saw old history")
	}
	if err := s.Append(ctx, replacement, absentUsage); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		id    EnrollmentID
		limit int
		want  []Observation
	}{
		{id, 20, []Observation{absentUsage, measuredZero}},
		{id, 1, []Observation{absentUsage}},
		{replacement, 20, []Observation{absentUsage}},
	} {
		got, err := s.Read(ctx, tt.id, now, now.Add(time.Second), tt.limit)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("read differs: %#v %v", got, err)
		}
	}
	if _, err := s.Read(ctx, id, now, now.Add(time.Second), maxHistoryRows+1); err == nil {
		t.Fatal("accepted query above row limit")
	}
}

func TestObservationStoreRejectsPrototypeWithoutAdopting(t *testing.T) {
	dir := persistentFixture(t)
	p, err := OpenPersistent(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, HistoryFilename)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if get, err := OpenObservations(dir); err == nil {
		_ = get.Close()
		t.Fatal("adopted prototype DB")
	}
	if err := InitializeObservations(dir); err == nil {
		t.Fatal("overwrote prototype DB")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || noSidecars(path) != nil {
		t.Fatal("prototype store mutated")
	}
}
