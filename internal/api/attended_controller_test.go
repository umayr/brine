package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/umayr/brine/internal/agent"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/dill"
	"github.com/umayr/brine/internal/httpx"
)

func TestAttendedFieldsClosedAndBounded(t *testing.T) {
	for _, raw := range []string{`{}`, `{"action":null}`, `{"action":2}`, `{"action":"up","action":"up"}`, `{"action":"up","extra":false}`, `{"action":"up"}{}`, `{"action":"up"}` + strings.Repeat(" ", 4097)} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		if _, err := attendedFields(req, "action"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"action":"up"}`))
	got, err := attendedFields(req, "action")
	if err != nil || got["action"] != "up" {
		t.Fatal(got, err)
	}
}

func TestAttendedConfirmationFieldsClosedAndRejectedBeforeConsumption(t *testing.T) {
	keys := []string{"requestId", "reviewDigest", "confirmationToken"}
	for _, key := range keys {
		values := map[string]string{"requestId": "a", "reviewDigest": "b", "confirmationToken": "c"}
		values[key] = strings.Repeat("x", 1024)
		body, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := attendedFields(httptest.NewRequest("POST", "/", strings.NewReader(string(body))), keys...); err != nil {
			t.Fatalf("1024-byte %s rejected by parser: %v", key, err)
		}
	}
	for _, key := range keys {
		for _, tc := range []struct {
			name  string
			value any
		}{{"null", nil}, {"number", 42}, {"bool", true}, {"array", []string{"x"}}, {"object", map[string]string{"x": "y"}}} {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				r, _, capture, _ := fakeFlow(t)
				valid := fakeReview(t, r)
				var fields map[string]any
				if err := json.Unmarshal([]byte(valid), &fields); err != nil {
					t.Fatal(err)
				}
				fields[key] = tc.value
				bad, _ := json.Marshal(fields)
				if w := fakeRequest(r, "/api/stacks/synthetic/up", string(bad)); w.Code != 400 {
					t.Fatalf("nonstring accepted: %d", w.Code)
				}
				if w := fakeRequest(r, "/api/stacks/synthetic/up", valid); w.Code != 200 || capture.applies.Load() != 1 {
					t.Fatal("nonstring consumed token")
				}
			})
		}
	}
	for _, name := range []string{"unknown", "duplicate", "null", "number", "missing", "trailing", "oversize", "requestId-long", "reviewDigest-long", "confirmationToken-long"} {
		t.Run(name, func(t *testing.T) {
			r, _, capture, _ := fakeFlow(t)
			valid := fakeReview(t, r)
			var fields map[string]string
			if err := json.Unmarshal([]byte(valid), &fields); err != nil {
				t.Fatal(err)
			}
			var bad string
			switch name {
			case "unknown":
				bad = strings.TrimSuffix(valid, "}") + `,"extra":"x"}`
			case "duplicate":
				bad = strings.TrimSuffix(valid, "}") + `,"requestId":"again"}`
			case "null":
				bad = strings.Replace(valid, `"requestId":"`+fields["requestId"]+`"`, `"requestId":null`, 1)
			case "number":
				bad = strings.Replace(valid, `"requestId":"`+fields["requestId"]+`"`, `"requestId":42`, 1)
			case "missing":
				delete(fields, "reviewDigest")
				b, _ := json.Marshal(fields)
				bad = string(b)
			case "trailing":
				bad = valid + `{}`
			case "oversize":
				bad = valid + strings.Repeat(" ", 4097)
			default:
				key := strings.TrimSuffix(name, "-long")
				fields[key] = strings.Repeat("x", 1025)
				b, _ := json.Marshal(fields)
				bad = string(b)
			}
			if w := fakeRequest(r, "/api/stacks/synthetic/up", bad); w.Code != 400 {
				t.Fatalf("invalid %s status %d: %s", name, w.Code, w.Body.String())
			}
			if capture.applies.Load() != 0 {
				t.Fatal("invalid DTO launched apply")
			}
			if w := fakeRequest(r, "/api/stacks/synthetic/up", valid); w.Code != 200 || capture.applies.Load() != 1 {
				t.Fatalf("invalid DTO consumed token: %d", w.Code)
			}
		})
	}
}

func TestAttendedUnavailableDeniesAllFiveAndLegacy(t *testing.T) {
	runner := dill.New("/must-not-execute")
	runner.Profile = "alpha"
	runner.Engine = "docker"
	runner.Socket = "/synthetic/socket"
	runner.QualifiedOperation = &dill.QualifiedOperationConfig{StateDir: "/synthetic/state", Layout: "native-local-docker-v1", DaemonID: "synthetic-daemon"}
	ag := agent.NewLocalAgent(&secretRouteEngine{}, runner)
	boundary, err := NewAttendedContext(ag, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	authn, err := auth.New(auth.Config{User: "operator", Password: "password", APIToken: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(authn.Middleware)
	if err := MountAttended(r, ag, nil, nil, nil, "docker", httpx.Timeouts{}, time.Second, boundary, nil); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"up", "start", "restart", "stop", "down"} {
		req := httptest.NewRequest("POST", "/api/stacks/demo/"+action, strings.NewReader(`{"requestId":"x","reviewDigest":"x","confirmationToken":"x"}`))
		req.Header.Set("Authorization", "Bearer synthetic-token")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 503 {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/stacks/demo/operations/preview", "/api/stacks/demo/teardown", "/api/containers/id/start", "/api/images/pull"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"action":"up"}`))
		req.Header.Set("Authorization", "Bearer synthetic-token")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 503 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	req := httptest.NewRequest("POST", "/api/stacks/demo/up", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
}
