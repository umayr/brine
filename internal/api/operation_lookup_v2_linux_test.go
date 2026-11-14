//go:build linux

package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
	"github.com/umayr/brine/internal/operationhistory"
)

func TestAuthenticatedLookupReturnsOriginalV2Record(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".brine-api-lookup-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	data, history, coord := filepath.Join(root, "data"), filepath.Join(root, "history"), filepath.Join(root, "coord")
	for _, p := range []string{data, history, coord} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s, err := operationhistory.Open(history, data, coord)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := operationhistory.Digest
	pending, err := s.BeginPreviewV2(operationhistory.BoundIntent{
		Action: "stop", Stack: "demo", Actor: d("actor"),
		Source:         operationhistory.SourceRef{SnapshotDigest: d("source"), EvaluatorProfileDigest: d("profile"), EvaluatorRefDigest: d("interpreter")},
		ExpectedTarget: dill.PreflightTarget{Engine: "docker", EndpointDigest: d("endpoint"), QualifiedIdentityDigest: d("daemon")},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(auth.Config{User: "operator", Password: "password", APIToken: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(a.Middleware)
	Mount(r, nil, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second, s)
	req := httptest.NewRequest("GET", "/api/operations/"+pending.ID(), nil)
	req.Header.Set("Authorization", "Bearer synthetic-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("lookup: %d %s", w.Code, w.Body.String())
	}
	var record operationhistory.RecordV2
	if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil || record.Version != operationhistory.VersionV2 || record.RequestID != pending.ID() || record.Phase != "accepted" {
		t.Fatalf("incorrect record: %s (%v)", w.Body.String(), err)
	}
	if strings.Contains(w.Body.String(), `"V2"`) || strings.Contains(w.Body.String(), `"V1"`) {
		t.Fatal("internal union exposed")
	}
}
