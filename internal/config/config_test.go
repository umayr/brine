package config

import "testing"

func TestLocalBindPolicy(t *testing.T) {
	c := Default()
	if c.Bind != "127.0.0.1" || c.NoAuth || c.Validate() != nil {
		t.Fatal("unsafe defaults")
	}
	for _, tc := range []struct {
		bind          string
		noAuth, valid bool
	}{
		{"127.0.0.1", true, true}, {"::1", true, true}, {"0.0.0.0", true, false}, {"::", true, false}, {"localhost", true, false}, {"0.0.0.0", false, true},
	} {
		c.Bind, c.NoAuth = tc.bind, tc.noAuth
		if (c.Validate() == nil) != tc.valid {
			t.Errorf("bind %q noauth %v", tc.bind, tc.noAuth)
		}
	}
}
