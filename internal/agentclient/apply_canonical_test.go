package agentclient

import (
	"strings"
	"testing"
)

func TestCanonicalDeploymentStatusKeys(t *testing.T) {
	base := `{"requestId":"id","stack":"s","state":"running","effects":"unknown"}`
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"canonical", base, true},
		{"optional operation", strings.TrimSuffix(base, "}") + `,"operationId":"op"}`, true},
		{"escaped canonical key", `{"sta\u0074e":"running"}`, true},
		{"request alias", strings.Replace(base, `"requestId"`, `"RequestId"`, 1), false},
		{"stack alias", strings.Replace(base, `"stack"`, `"Stack"`, 1), false},
		{"state alias", strings.Replace(base, `"state"`, `"State"`, 1), false},
		{"effects alias", strings.Replace(base, `"effects"`, `"Effects"`, 1), false},
		{"operation alias", strings.TrimSuffix(base, "}") + `,"OperationId":"op"}`, false},
		{"alias alongside canonical", strings.TrimSuffix(base, "}") + `,"State":"failed"}`, false},
		{"unknown", strings.TrimSuffix(base, "}") + `,"extra":1}`, false},
		{"null", `null`, false},
		{"array", `[]`, false},
		{"malformed", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonicalDeploymentStatusKeys([]byte(tc.body)); got != tc.want {
				t.Fatalf("canonicalDeploymentStatusKeys(%s) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
