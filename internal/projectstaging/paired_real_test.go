package projectstaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/umayr/brine/internal/dillserve"
	"github.com/umayr/brine/internal/projectreview"
	"github.com/umayr/brine/internal/projectsnapshot"
)

// TestRealDillServe is opt-in: provide an independently built dill-serve binary.
// It uses only private temporary directories and a fake Unix Docker endpoint.
func TestRealDillServe(t *testing.T) {
	bin := os.Getenv("DILL_SERVE_TEST_BIN")
	if bin == "" {
		t.Skip("set DILL_SERVE_TEST_BIN to a dill-serve binary")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("DILL_SERVE_TEST_BIN must be absolute")
	}
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "brine-pair-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	stage := filepath.Join(root, "stages")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(root, "catalog")
	if err := os.Mkdir(catalog, 0700); err != nil {
		t.Fatal(err)
	}
	dockerSock, serviceSock := filepath.Join(root, "d.sock"), filepath.Join(root, "s.sock")
	l, err := net.Listen("unix", dockerSock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	fake := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
		case (r.Method == "GET" || r.Method == "HEAD") && r.URL.Path == "/_ping":
			w.Header().Set("API-Version", "1.41")
			fmt.Fprint(w, "OK")
		default:
			http.Error(w, "unexpected", http.StatusForbidden)
		}
	})}
	go fake.Serve(l)
	t.Cleanup(func() { fake.Close() })
	marker := filepath.Join(root, "pkl-ran")
	pkl := filepath.Join(root, "pkl")
	if err := os.WriteFile(pkl, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-engine", "docker", "-endpoint", "unix://"+dockerSock, "-stack", "stack", "-socket", serviceSock, "-staging-root", stage, "-staging-catalog", catalog)
	cmd.Env = []string{"PATH=" + root + ":/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root}
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(serviceSock); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("service exited: %v: %s", err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("service socket not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx := context.Background()
	store, err := projectreview.New(20, 1<<20, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := projectreview.Current{Stack: "stack", Target: projectreview.Target{Kind: projectreview.Local, Identity: "target", Socket: serviceSock}, Options: map[string]string{"engine": "docker"}, Variables: map[string]string{"region": "one"}}
	s, err := New(store, dillserve.Target{Socket: serviceSock, Stack: "stack", Engine: "docker"}, "target", func(context.Context) (projectreview.Current, error) { return c, nil })
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{"empty": {}, "main.pkl": []byte("this is deliberately not valid Pkl\n"), "sub/a&b": {0, 1, 2, 255}}
	snap := projectsnapshot.Snapshot{Entrypoint: "main.pkl"}
	for _, p := range []string{"empty", "main.pkl", "sub/a&b"} {
		b := contents[p]
		snap.Files = append(snap.Files, projectsnapshot.File{Path: p, Content: b, SHA256: sha256.Sum256(b)})
	}
	preview, err := s.Create(ctx, "session", snap)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Entrypoint != "main.pkl" || len(preview.Files) != 3 || preview.Current.Variables["region"] != "one" {
		t.Fatalf("bad preview: %+v", preview)
	}
	for _, f := range preview.Files {
		b, ok := contents[f.Path]
		if !ok || f.Size != int64(len(b)) || f.SHA256 != sha256.Sum256(b) {
			t.Fatalf("bad metadata: %+v", f)
		}
	}
	encoded, _ := json.Marshal(preview)
	if bytes.Contains(encoded, contents["main.pkl"]) {
		t.Fatal("preview contains file bytes")
	}
	if _, err = s.Stage(ctx, "session", preview.ID, ""); err == nil {
		t.Fatal("unconfirmed stage succeeded")
	}
	entries, err := os.ReadDir(stage)
	if err != nil || len(entries) != 0 {
		t.Fatalf("pre-confirm stage: %v %v", entries, err)
	}
	token, err := s.Confirm(ctx, "session", preview.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Stage(ctx, "session", preview.ID, token)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Fatalf("invalid id %q", id)
	}
	if _, err = s.Stage(ctx, "session", preview.ID, token); err == nil {
		t.Fatal("replay succeeded")
	}
	dir := filepath.Join(stage, "snapshot-"+id)
	found := map[string]bool{}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(dir, p)
		info, e := d.Info()
		if e != nil {
			return e
		}
		if d.IsDir() {
			if rel != "." && rel != "sub" {
				return fmt.Errorf("unexpected directory %s", rel)
			}
			if info.Mode().Perm() != 0700 {
				return fmt.Errorf("directory %s mode %v", rel, info.Mode())
			}
			return nil
		}
		b, ok := contents[filepath.ToSlash(rel)]
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fmt.Errorf("unexpected file %s mode %v", rel, info.Mode())
		}
		got, e := os.ReadFile(p)
		if e != nil || !bytes.Equal(got, b) {
			return fmt.Errorf("bytes %s: %v", rel, e)
		}
		found[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil || len(found) != len(contents) {
		t.Fatalf("staged tree: %v %v", found, err)
	}
	// Exercise the real sender, service catalog, and Brine manifest client in
	// sequence, including the wire encoding of HTML-sensitive path characters.
	client, err := dillserve.NewLocal(dillserve.Target{Socket: serviceSock, Stack: "stack", Engine: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	manifest, err := client.Snapshot(ctx, id)
	if err != nil {
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", serviceSock)
		}}
		defer transport.CloseIdleConnections()
		diagnostic := &http.Client{Transport: transport}
		for _, path := range []string{"/v1/health", "/v1/snapshots/" + id} {
			resp, e := diagnostic.Get("http://local" + path)
			if e != nil {
				t.Logf("%s: %v", path, e)
				continue
			}
			var body bytes.Buffer
			_, _ = body.ReadFrom(resp.Body)
			resp.Body.Close()
			t.Logf("%s: HTTP %d %s", path, resp.StatusCode, body.String())
		}
		t.Fatalf("catalog snapshot: %v", err)
	}
	type expectedFile struct {
		Path   string `json:"path"`
		Length int    `json:"length"`
		SHA256 string `json:"sha256"`
	}
	wire := struct {
		Schema     string         `json:"schema"`
		Entrypoint string         `json:"entrypoint"`
		Files      []expectedFile `json:"files"`
	}{Schema: "dill.snapshot.manifest.v1", Entrypoint: "main.pkl"}
	for _, name := range []string{"empty", "main.pkl", "sub/a&b"} {
		b := contents[name]
		wire.Files = append(wire.Files, expectedFile{name, len(b), fmt.Sprintf("%x", sha256.Sum256(b))})
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`\u0026`)) || manifest.Raw() != string(raw) || manifest.Digest() != sha256.Sum256(raw) || manifest.Entrypoint() != wire.Entrypoint {
		t.Fatalf("catalog manifest mismatch: %s", manifest.Raw())
	}
	files := manifest.Files()
	if len(files) != len(wire.Files) {
		t.Fatalf("catalog file count: %d", len(files))
	}
	for i, want := range wire.Files {
		b := contents[want.Path]
		if files[i].Path != want.Path || files[i].Size != int64(len(b)) || files[i].SHA256 != sha256.Sum256(b) {
			t.Fatalf("catalog file %d: %+v", i, files[i])
		}
	}
	for _, target := range []dillserve.Target{{Socket: serviceSock, Stack: "wrong", Engine: "docker"}, {Socket: serviceSock, Stack: "stack", Engine: "podman"}} {
		other, e := dillserve.NewLocal(target)
		if e != nil {
			t.Fatal(e)
		}
		_, e = other.Snapshot(ctx, id)
		other.Close()
		if e == nil {
			t.Fatalf("catalog accepted wrong target: %+v", target)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a&b"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Snapshot(ctx, id); err == nil {
		t.Fatal("catalog accepted corrupted stage")
	}
	for _, change := range []string{"target", "variables"} {
		p, e := s.Create(ctx, "session", snap)
		if e != nil {
			t.Fatal(e)
		}
		tok, e := s.Confirm(ctx, "session", p.ID)
		if e != nil {
			t.Fatal(e)
		}
		if change == "target" {
			c.Target.Identity = "other"
		} else {
			c.Variables["region"] = "two"
		}
		if _, e = s.Stage(ctx, "session", p.ID, tok); e == nil {
			t.Fatalf("%s mismatch accepted", change)
		}
		c.Target.Identity = "target"
		c.Variables["region"] = "one"
		if _, e = s.Stage(ctx, "session", p.ID, tok); e == nil {
			t.Fatalf("%s mismatch replay accepted", change)
		}
	}
	bad := []byte("x")
	invalid := projectsnapshot.Snapshot{Entrypoint: "CON", Files: []projectsnapshot.File{{Path: "CON", Content: bad, SHA256: sha256.Sum256(bad)}}}
	if _, err = s.Create(ctx, "session", invalid); err == nil {
		t.Fatal("unsupported receiver path accepted")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	entries, err = os.ReadDir(stage)
	if err != nil || len(entries) != 1 || entries[0].Name() != "snapshot-"+id {
		t.Fatalf("retention/unexpected stages: %v %v", entries, err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Pkl execution marker: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("fake engine never received status request")
	}
	for _, r := range requests {
		if !(strings.HasPrefix(r, "GET ") && strings.HasSuffix(r, "/containers/json")) && r != "GET /_ping" && r != "HEAD /_ping" {
			t.Errorf("unexpected engine request: %s", r)
		}
	}
}
