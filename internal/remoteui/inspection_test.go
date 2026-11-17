package remoteui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func inspectionFixture() *Inspection {
	s := `hostile <img src=x onerror=alert(1)>`
	zero := int64(0)
	no := false
	return &Inspection{ID: testID, Name: s, Stack: s, Service: "service", ObservedAt: "2025-01-01T00:00:00Z", Image: "image:tag", ImageID: "sha256:image", State: "running", CreatedAt: &s, User: &s, Command: []string{s}, EnvKeys: []string{"TOKEN"}, Labels: map[string]string{"dill.managed": "true", "dill.stack": s, "dill.service": "service"}, Ports: []InspectionPort{{Protocol: s, HostIP: s, HostPort: 8080, ContainerPort: 80}}, Mounts: []InspectionMount{{Type: "bind", Source: s, Destination: "/app", ReadOnly: true}}, Networks: []string{s}, RestartPolicy: &s, Health: InspectionHealth{Status: &s, Test: []string{s}}, Resources: InspectionResources{MemoryBytes: &zero, CPUNano: &zero, PidsLimit: &zero}, Security: InspectionSecurity{Privileged: &no, CapAdd: []string{s}, NetworkMode: &s, PIDMode: &s, IPCMode: &s}}
}

func TestInspectionProfileEscaped(t *testing.T) {
	_, f, h, c, _ := setup(t)
	f.inspection = inspectionFixture()
	r := httptest.NewRequest("GET", "/remote/inspect?host=h&container="+testID, nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d", w.Code)
	}
	for _, value := range []string{"image:tag", "sha256:image", "running", "2025-01-01", "8080", "/app", "read-only", "dill.managed", "TOKEN", "&lt;img", ">0</dd>", ">false</dd>"} {
		if !strings.Contains(body, value) {
			t.Errorf("missing %q", value)
		}
	}
	if strings.Contains(body, "<img src=x") || strings.Contains(body, "secret-value") {
		t.Fatal("unsafe or secret content in inspection")
	}
}

func TestInspectionRejectsUnsafeEnvironmentAndLabels(t *testing.T) {
	const sentinel = "sensitive-sentinel-value"
	tests := []struct {
		name   string
		mutate func(*Inspection)
	}{
		{"assignment", func(i *Inspection) { i.EnvKeys = []string{"TOKEN=" + sentinel} }},
		{"empty name", func(i *Inspection) { i.EnvKeys = []string{""} }},
		{"duplicate name", func(i *Inspection) { i.EnvKeys = []string{"TOKEN", "TOKEN"} }},
		{"unsorted names", func(i *Inspection) { i.EnvKeys = []string{"Z", "A"} }},
		{"unexpected label", func(i *Inspection) { i.Labels["secret"] = sentinel }},
		{"missing label", func(i *Inspection) { delete(i.Labels, "dill.managed") }},
		{"mismatched ownership", func(i *Inspection) { i.Labels["dill.stack"] = sentinel }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, f, h, c, _ := setup(t)
			f.inspection = inspectionFixture()
			tc.mutate(f.inspection)
			r := httptest.NewRequest("GET", "/remote/inspect?host=h&container="+testID, nil)
			r.AddCookie(c)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			body := w.Body.String()
			if !strings.Contains(body, "Inspection unavailable") || strings.Contains(body, sentinel) || strings.Contains(body, "image:tag") {
				t.Fatalf("invalid inspection rendered: %s", body)
			}
		})
	}
}
