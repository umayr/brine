package projectreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"github.com/umayr/brine/internal/projectsnapshot"
)

func fixture() Intent {
	b := []byte("SECRET-SENTINEL-CONTENT")
	return Intent{Snapshot: projectsnapshot.Snapshot{Entrypoint: "main.pkl", Files: []projectsnapshot.File{{Path: "main.pkl", Content: b, SHA256: sha256.Sum256(b)}}}, Current: Current{Stack: "fixed", Target: Target{Kind: Local, Identity: "host", Socket: "/run/dill.sock"}, Variables: map[string]string{"x": "y"}, Options: map[string]string{"a": "b"}}}
}
func store(t *testing.T) *Store {
	t.Helper()
	s, e := New(2, 10000, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func TestTargets(t *testing.T) {
	pin := "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	for _, tc := range []struct {
		target Target
		ok     bool
	}{{Target{Kind: Local, Identity: "local", Socket: "/run/dill.sock"}, true}, {Target{Kind: SSH, Identity: "remote", Socket: "/run/dill.sock", Endpoint: "user@example:22", SSHKnownHost: pin}, true}, {Target{Kind: Local, Identity: "l", Socket: "s", SSHKnownHost: pin}, false}, {Target{Kind: Local, Identity: "l", Socket: "s", Endpoint: "host"}, false}, {Target{Kind: SSH, Identity: "r", Socket: "s", SSHKnownHost: pin}, false}, {Target{Kind: SSH, Identity: "r", Socket: "s", Endpoint: "host", SSHKnownHost: "dummy"}, false}, {Target{Kind: SSH, Identity: "r", Socket: "s", Endpoint: "host", SSHKnownHost: pin + "="}, false}, {Target{Identity: "x", Socket: "s"}, false}} {
		in := fixture()
		in.Current.Target = tc.target
		s := store(t)
		_, e := s.Create(context.Background(), "s", in)
		if (e == nil) != tc.ok {
			t.Fatalf("%+v: %v", tc.target, e)
		}
	}
}
func TestPreviewNoContentAndCopies(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	in := fixture()
	p, e := s.Create(ctx, "s", in)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(p)
	var buf bytes.Buffer
	template.Must(template.New("p").Parse("{{printf \"%+v\" .}}")).Execute(&buf, p)
	for _, out := range []string{string(raw), buf.String(), fmt.Sprintf("%+v", p)} {
		if strings.Contains(out, "SECRET-SENTINEL-CONTENT") || strings.Contains(out, base64.StdEncoding.EncodeToString(in.Snapshot.Files[0].Content)) {
			t.Fatal("content exposed")
		}
	}
	if p.Files[0].Size != int64(len(in.Snapshot.Files[0].Content)) || p.Files[0].SHA256 != in.Snapshot.Files[0].SHA256 {
		t.Fatal(p.Files)
	}
	expected := fixture()
	in.Snapshot.Files[0].Content[0] = 'X'
	in.Current.Variables["x"] = "changed"
	p.Current.Options["a"] = "changed"
	p.Files[0].Path = "wrong"
	token, e := s.Confirm(ctx, "s", p.ID, expected.Current)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Consume(ctx, "s", p.ID, token, expected.Current)
	if e != nil || !bytes.Equal(got.Snapshot.Files[0].Content, expected.Snapshot.Files[0].Content) {
		t.Fatal(e)
	}
}
func TestExactCurrentReplayAndSession(t *testing.T) {
	ctx := context.Background()
	s := store(t)
	in := fixture()
	p, e := s.Create(ctx, "session", in)
	if e != nil {
		t.Fatal(e)
	}
	mutations := []func(*Current){func(c *Current) { c.Stack = "other" }, func(c *Current) { c.Target.Identity = "other" }, func(c *Current) { c.Target.Socket = "other" }, func(c *Current) { c.Target.Kind = SSH }, func(c *Current) { c.Target.Endpoint = "other" }, func(c *Current) { c.Target.SSHKnownHost = "other" }, func(c *Current) { c.Variables["x"] = "z" }, func(c *Current) { c.Options["a"] = "z" }}
	for _, mut := range mutations {
		c := copyCurrent(in.Current)
		mut(&c)
		if _, e = s.Confirm(ctx, "session", p.ID, c); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if _, e = s.Confirm(ctx, "session", p.ID, in.Current); !errors.Is(e, ErrInvalid) {
			t.Fatal("reverted confirmation accepted", e)
		}
		p, e = s.Create(ctx, "session", in)
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.Confirm(ctx, "wrong", p.ID, in.Current); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	token, e := s.Confirm(ctx, "session", p.ID, in.Current)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Confirm(ctx, "session", p.ID, in.Current); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	for _, mut := range mutations {
		c := copyCurrent(in.Current)
		mut(&c)
		if _, e = s.Consume(ctx, "session", p.ID, token, c); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if _, e = s.Consume(ctx, "session", p.ID, token, in.Current); !errors.Is(e, ErrInvalid) {
			t.Fatal("reverted consumption accepted", e)
		}
		p, e = s.Create(ctx, "session", in)
		if e != nil {
			t.Fatal(e)
		}
		token, e = s.Confirm(ctx, "session", p.ID, in.Current)
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, a := range [][3]string{{"wrong", p.ID, token}, {"session", "unknown", token}, {"session", p.ID, "bad"}} {
		if _, e = s.Consume(ctx, a[0], a[1], a[2], in.Current); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, e = s.Consume(ctx, "session", p.ID, token, in.Current); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Consume(ctx, "session", p.ID, token, in.Current); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
func TestRaces(t *testing.T) {
	ctx := context.Background()
	s := store(t)
	in := fixture()
	p, _ := s.Create(ctx, "s", in)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, e := s.Confirm(ctx, "s", p.ID, in.Current)
			if e == nil {
				wins.Add(1)
				if _, e = s.Consume(ctx, "s", p.ID, token, in.Current); e != nil {
					t.Error(e)
				}
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
	p, _ = s.Create(ctx, "s", in)
	token, _ := s.Confirm(ctx, "s", p.ID, in.Current)
	wins.Store(0)
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Consume(ctx, "s", p.ID, token, in.Current); e == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal(wins.Load())
	}
}
func TestBoundsAndValidation(t *testing.T) {
	ctx := context.Background()
	s := store(t)
	in := fixture()
	huge := fixture()
	huge.Snapshot.Files[0].Content = make([]byte, 20000)
	if _, e := s.Create(ctx, "s", huge); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	many := fixture()
	many.Snapshot.Files = make([]projectsnapshot.File, 10000)
	if _, e := s.Create(ctx, "s", many); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	in.Snapshot.Files[0].SHA256 = [32]byte{}
	if _, e := s.Create(ctx, "s", in); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	in = fixture()
	p, _ := s.Create(ctx, "s", in)
	_, e := s.Create(ctx, "s", in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Create(ctx, "s", in); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	if _, e = s.Confirm(ctx, "s", "unknown", Current{Variables: map[string]string{"huge": strings.Repeat("x", 100000)}}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e = s.Consume(ctx, "s", p.ID, strings.Repeat("x", 100000), in.Current); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
func TestExpiryCloseAndCancellation(t *testing.T) {
	ctx := context.Background()
	s, e := New(2, 10000, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	in := fixture()
	p, _ := s.Create(ctx, "s", in)
	token, _ := s.Confirm(ctx, "s", p.ID, in.Current)
	deadline := time.After(3 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.entries)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("idle expiry did not sweep")
		case <-time.After(time.Millisecond):
		}
	}
	if _, e = s.Consume(ctx, "s", p.ID, token, in.Current); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.Create(canceled, "s", in); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	p, _ = s.Create(ctx, "s", in)
	if _, e = s.Confirm(canceled, "s", p.ID, in.Current); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	token, _ = s.Confirm(ctx, "s", p.ID, in.Current)
	if _, e = s.Consume(canceled, "s", p.ID, token, in.Current); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	s.Close()
	s.Close()
	if _, e = s.Consume(ctx, "s", p.ID, token, in.Current); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
}

// Cancellation is observed while hashing, not only before validation starts.
type cancelAfter struct {
	context.Context
	calls atomic.Int32
}

func (c *cancelAfter) Err() error {
	if c.calls.Add(1) > 3 {
		return context.Canceled
	}
	return nil
}
func TestCancellationDuringHash(t *testing.T) {
	s, e := New(2, 2<<20, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	in := fixture()
	in.Snapshot.Files[0].Content = make([]byte, 1<<20)
	in.Snapshot.Files[0].SHA256 = sha256.Sum256(in.Snapshot.Files[0].Content)
	ctx := &cancelAfter{Context: context.Background()}
	if _, e = s.Create(ctx, "s", in); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

// gateContext pauses validation after admission so competitors race a live reservation.
type gateContext struct {
	context.Context
	calls            atomic.Int32
	entered, release chan struct{}
}

func (g *gateContext) Err() error {
	if g.calls.Add(1) == 2 {
		close(g.entered)
		<-g.release
	}
	return g.Context.Err()
}
func TestAdmissionReservation(t *testing.T) {
	for _, closeDuring := range []bool{false, true} {
		t.Run(fmt.Sprint(closeDuring), func(t *testing.T) {
			s, err := New(1, 10000, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gate := &gateContext{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
			result := make(chan error, 1)
			go func() { _, err := s.Create(gate, "s", fixture()); result <- err }()
			<-gate.entered
			var wg sync.WaitGroup
			for range 20 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := s.Create(context.Background(), "other", fixture()); !errors.Is(err, ErrCapacity) {
						t.Errorf("competing admission: %v", err)
					}
				}()
			}
			wg.Wait()
			cancel()
			if closeDuring {
				s.Close()
			}
			close(gate.release)
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			s.mu.Lock()
			count, used := s.reservedCount, s.reservedBytes
			s.mu.Unlock()
			if count != 0 || used != 0 {
				t.Fatalf("reservation leaked: %d %d", count, used)
			}
			if !closeDuring {
				if _, err := s.Create(context.Background(), "s", fixture()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestMinimumTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Nanosecond, time.Second - 1} {
		if s, e := New(1, 10000, ttl); !errors.Is(e, ErrInvalid) {
			if s != nil {
				s.Close()
			}
			t.Fatal(ttl, e)
		}
	}
	s, e := New(1, 10000, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
}

func TestAdmissionByteReservation(t *testing.T) {
	in := fixture()
	n, ok := budget(in, "s", 10000)
	if !ok {
		t.Fatal("budget")
	}
	s, err := New(2, n*2-1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	gate := &gateContext{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	result := make(chan error, 1)
	go func() { _, err := s.Create(gate, "s", in); result <- err }()
	<-gate.entered
	if _, err = s.Create(context.Background(), "s", in); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	cancel()
	close(gate.release)
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.Create(context.Background(), "s", in); err != nil {
		t.Fatal(err)
	}
}

// One validation remains paused while another transfers its reservation to a
// retained entry. The remaining slot and bytes must be available immediately.
func TestReservationTransferWithConcurrentAdmission(t *testing.T) {
	in := fixture()
	n, ok := budget(in, "s", 10000)
	if !ok {
		t.Fatal("budget")
	}
	s, err := New(3, n*3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gate := &gateContext{Context: context.Background(), entered: make(chan struct{}), release: make(chan struct{})}
	result := make(chan error, 1)
	go func() { _, err := s.Create(gate, "s", in); result <- err }()
	<-gate.entered
	if _, err = s.Create(context.Background(), "s", in); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	entries, reserved, used, held := len(s.entries), s.reservedCount, s.used, s.reservedBytes
	s.mu.Unlock()
	if entries != 1 || reserved != 1 || used != n || held != n {
		t.Fatalf("incorrect atomic accounting: %d %d %d %d", entries, reserved, used, held)
	}
	if _, err = s.Create(context.Background(), "s", in); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	entries, reserved, used, held = len(s.entries), s.reservedCount, s.used, s.reservedBytes
	s.mu.Unlock()
	if entries != 3 || reserved != 0 || used != 3*n || held != 0 {
		t.Fatalf("incorrect final accounting: %d %d %d %d", entries, reserved, used, held)
	}
}
