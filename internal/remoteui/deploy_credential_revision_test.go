package remoteui

import (
	"context"
	"errors"
	"testing"
)

func TestReviewedCredentialCannotBeReplacedBetweenCheckAndConnection(t *testing.T) {
	_, _, _, row, scope := credentialFixture(t)
	ctx := context.WithValue(context.Background(), deploymentEnrollmentKey{}, scope)
	h := &Deployment{credentials: testDeploymentCredentials("first-reviewed-material")}
	reviewed := h.deploymentCredentialRevision(ctx, row)
	h.credentials = testDeploymentCredentials("replacement-material")
	if _, _, e := h.openDeploymentSession(ctx, row, reviewed); !errors.Is(e, errDeployCredential) {
		t.Fatal("replacement credential reached connection instead of reviewed-revision denial", e)
	}
}
func TestDeploymentCredentialIndexRejectsCaseAliases(t *testing.T) {
	for _, body := range []string{`{"Version":1,"targets":{}}`, `{"version":1,"targets":{"host":{"binding":"x","enrollment":"y","token_file":"a","Token_File":"b"}}}`} {
		if uniqueCredentialJSON([]byte(body)) {
			t.Fatal("case-insensitive alias bypassed exact custody schema")
		}
	}
}
