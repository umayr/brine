package config

import "testing"

func TestLocalStagingValidation(t *testing.T) {
	for _, tc := range []struct {
		socket, identity string
		noAuth, ok       bool
	}{
		{"", "", false, true}, {"/tmp/service.sock", "target-1", false, true},
		{"/tmp/service.sock", "", false, false}, {"", "target", false, false},
		{"relative", "target", false, false}, {"/tmp/../service.sock", "target", false, false},
		{"/tmp/service.sock", " target", false, false}, {"/tmp/service.sock", "target", true, false},
		{"ssh://host/socket", "target", false, false},
	} {
		c := Default()
		c.StagingSocket = tc.socket
		c.StagingIdentity = tc.identity
		c.NoAuth = tc.noAuth
		if (c.Validate() == nil) != tc.ok {
			t.Errorf("socket=%q identity=%q noAuth=%v", tc.socket, tc.identity, tc.noAuth)
		}
	}
}
