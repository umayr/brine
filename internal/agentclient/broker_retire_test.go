package agentclient

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrokerRetireGrantNotInherited(t *testing.T) {
	r := httptest.NewRequest("POST", "https://target/v1/broker-secrets/retire/"+strings.Repeat("a", 32), nil)
	for _, g := range []BrokerGrants{BrokerPrepare, BrokerApply | BrokerReview, BrokerStage | BrokerStatus} {
		if allowedBrokerDeploy(r, g) {
			t.Fatal("retirement authority inherited")
		}
	}
	if !allowedBrokerDeploy(r, BrokerRetire) {
		t.Fatal("explicit retire grant refused")
	}
	r = httptest.NewRequest("POST", "https://target/v1/broker-secrets/prepare/"+strings.Repeat("a", 32), nil)
	if allowedBrokerDeploy(r, BrokerRetire) {
		t.Fatal("retire grant enabled value preparation")
	}
}
