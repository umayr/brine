package dillserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const catalogID = "0123456789abcdef0123456789abcdef"

func envelope(raw string) string {
	d := sha256.Sum256([]byte(raw))
	return fmt.Sprintf(`{"stage_id":"%s","manifest":%s,"manifest_sha256":"%x"}`, catalogID, raw, d)
}
func catalogClient(t *testing.T, health, status, body string) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			fmt.Fprint(w, health)
		case "/v1/status":
			fmt.Fprint(w, status)
		case "/v1/snapshots/" + catalogID:
			fmt.Fprint(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &Client{target: Target{Stack: "demo", Engine: "docker"}, transport: tr, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = strings.TrimPrefix(s.URL, "http://")
		return tr.RoundTrip(r)
	})}}
}

func TestCatalog(t *testing.T) {
	h := `{"api_version":"v1","capabilities":["health","status","snapshot-manifest"]}`
	raw := `{"schema":"dill.snapshot.manifest.v1","entrypoint":"main.pkl","files":[{"path":"main.pkl","length":3,"sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}]}`
	good := envelope(raw)
	cases := []struct {
		name, health, status, body, id string
		ok                             bool
	}{
		{"good", h, goodStatus, good, catalogID, true},
		{"capability", goodHealth, goodStatus, good, catalogID, false},
		{"target", h, `{"stack":"changed","engine":"docker","containers":[]}`, good, catalogID, false},
		{"id", h, goodStatus, strings.Replace(good, catalogID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1), catalogID, false},
		{"bad id", h, goodStatus, good, "bad", false},
		{"duplicate", h, goodStatus, strings.Replace(good, `"stage_id":`, `"stage_id":"x","stage_id":`, 1), catalogID, false},
		{"unknown", h, goodStatus, strings.Replace(good, `"stage_id":`, `"extra":1,"stage_id":`, 1), catalogID, false},
		{"missing", h, goodStatus, `{}`, catalogID, false},
		{"tampered", h, goodStatus, strings.Replace(good, "main.pkl", "other.pkl", 1), catalogID, false},
		{"oversized", h, goodStatus, strings.Repeat(" ", 4<<20) + good, catalogID, false},
		{"manifest unknown", h, goodStatus, envelope(strings.Replace(raw, `"schema":`, `"unknown":1,"schema":`, 1)), catalogID, false},
		{"manifest duplicate", h, goodStatus, envelope(strings.Replace(raw, `"schema":`, `"schema":"x","schema":`, 1)), catalogID, false},
		{"bad path", h, goodStatus, envelope(strings.ReplaceAll(raw, "main.pkl", "../x")), catalogID, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := catalogClient(t, tc.health, tc.status, tc.body)
			m, err := c.Snapshot(context.Background(), tc.id)
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected result: %v", err)
			}
			if tc.ok {
				if m.Raw() != raw || len(m.Files()) != 1 {
					t.Fatal("wrong manifest")
				}
				f := m.Files()
				f[0].Path = "changed"
				if m.Files()[0].Path != "main.pkl" {
					t.Fatal("mutable result")
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := catalogClient(t, h, goodStatus, good)
	if _, err := c.Snapshot(ctx, catalogID); err == nil {
		t.Fatal("cancel accepted")
	}
}
func TestCatalogEscapedLargeManifest(t *testing.T) {
	h := `{"api_version":"v1","capabilities":["health","status","snapshot-manifest"]}`
	files := make([]manifestWireFile, 1000)
	hash := sha256.Sum256(nil)
	for i := range files {
		p := fmt.Sprintf("%s/%04d%s", strings.Repeat("&", 245), i, strings.Repeat("&", 245))
		files[i] = manifestWireFile{p, 0, hex.EncodeToString(hash[:])}
	}
	w := manifestWire{"dill.snapshot.manifest.v1", files[0].Path, files}
	raw, _ := json.Marshal(w)
	if len(raw) < 2<<20 || len(envelope(string(raw))) > 4<<20 {
		t.Fatal("bad fixture size")
	}
	c := catalogClient(t, h, goodStatus, envelope(string(raw)))
	m, err := c.Snapshot(context.Background(), catalogID)
	if err != nil || len(m.Files()) != 1000 {
		t.Fatalf("large manifest rejected: %v", err)
	}
}
