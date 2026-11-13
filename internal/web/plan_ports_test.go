package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/umayr/brine/internal/dill"
)

func TestDescribePlanPort(t *testing.T) {
	tests := []struct{ field, value, want string }{
		{"port.80/tcp", "80", "Container port 80 / TCP; no host binding reported"},
		{"port.80/tcp", "3001->80", "Default host interface · host port 3001 → container port 80 / TCP"},
		{"port.8081/tcp", ":::80->8081", "All IPv6 interfaces (::) · host port 80 → container port 8081 / TCP"},
		{"port.53/udp", "0.0.0.0:53->53", "All IPv4 interfaces (0.0.0.0) · host port 53 → container port 53 / UDP"},
		{"port.80/tcp", "127.0.0.1:8080->80", "Host address 127.0.0.1 · host port 8080 → container port 80 / TCP"},
		{"port.80/tcp", "::1:8080->80", "Host address ::1 · host port 8080 → container port 80 / TCP"},
		{"port.80/sctp", "8080->80", "Default host interface · host port 8080 → container port 80 / SCTP"},
		{"port.80/tcp", "80->81", "Unrecognized port value: 80->81"},
		{"port.80/tcp", "auto->80", "Unrecognized port value: auto->80"},
		{"port.80/tcp", "[::1]:80->80", "Unrecognized port value: [::1]:80->80"},
		{"port.80/unknown", "8080->80", "Unrecognized port value: 8080->80"},
		{"port.80/tcp", "65536->80", "Unrecognized port value: 65536->80"},
		{"port.80/tcp", "<script>", "Unrecognized port value: <script>"},
	}
	for _, tt := range tests {
		t.Run(tt.field+"/"+tt.value, func(t *testing.T) {
			if got := describePlanPort(tt.field, tt.value); got != tt.want {
				t.Errorf("describePlanPort(%q, %q) = %q; want %q", tt.field, tt.value, got, tt.want)
			}
		})
	}
}

func TestPlanDiffPortAndFingerprintPresentation(t *testing.T) {
	ts, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	result := &dill.PlanResult{Changes: []dill.PlanChange{{Service: "web", Kind: "recreate", Diffs: []dill.FieldDiff{
		{Field: "config_hash", Before: "oldhash", After: "newhash"},
		{Field: "port.80/tcp", Before: "80", After: "3001->80"},
		{Field: "port.8081/tcp", Before: ":::80->8081"},
		{Field: "port.90/tcp", After: "<script>alert(1)</script>"},
	}}}, Summary: dill.PlanSummary{Recreate: 1}}
	var buf bytes.Buffer
	if err := ts.partials["plan-diff"].Execute(&buf, result); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Configuration fingerprint changed", "<details", "oldhash", "Container port 80 / TCP; no host binding reported", "host port 3001 → container port 80 / TCP", "All IPv6 interfaces (::)", "Raw Dill port values", "&lt;script&gt;"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatal("port value was not escaped")
	}
}

func TestDescribePlanPortNeverGuessesUnknownInput(t *testing.T) {
	for _, value := range []string{"", "80:90->80", "1->80->90", "-1->80", "0->80", "1-2->80"} {
		if got := describePlanPort("port.80/tcp", value); !strings.HasPrefix(got, "Unrecognized port value:") {
			t.Errorf("%q unexpectedly interpreted as %q", value, got)
		}
	}
}
