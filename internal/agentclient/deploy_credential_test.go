package agentclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDeploymentCredentialNeverFallsBackOrCrossesRoutes(t *testing.T) {
	c := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected real request") })
	c.bearer = "monitor"
	calls := 0
	c.http.Transport = deploymentLookupTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		expected := "monitor"
		if deploymentRoute(r) {
			expected = "deploy"
		}
		if r.Header.Get("Authorization") != "Bearer "+expected {
			t.Fatal("credential crossed role boundary")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
	})
	req, _ := http.NewRequestWithContext(context.Background(), "POST", c.baseURL+"/v1/snapshots", nil)
	if _, e := c.do(req); e == nil || calls != 0 {
		t.Fatal("missing deploy credential fell back to monitor")
	}
	c.deployBearer = "deploy"
	if _, e := c.do(req); e != nil {
		t.Fatal(e)
	}
	read, _ := http.NewRequest("POST", c.baseURL+"/v1/session", nil)
	if _, e := c.do(read); e != nil {
		t.Fatal(e)
	}
	bad, _ := http.NewRequest("POST", c.baseURL+"/v1/deploy/unknown", nil)
	if _, e := c.do(bad); e == nil || calls != 2 {
		t.Fatal("unknown deployment route received credential")
	}
}
func TestDeploymentCredentialRejectsMissingIdenticalAndNoncanonical(t *testing.T) {
	monitor := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	for _, bad := range []string{"", monitor, monitor + "="} {
		if validDeployCredential(monitor, bad) {
			t.Fatal("invalid deploy credential accepted")
		}
	}
}
