package target

import "testing"

func TestExplicitAlphaTargetAndLegacyCompatibility(t *testing.T) {
	for _, engine := range []string{"docker", "podman"} {
		for _, socket := range []string{"/synthetic/engine.sock", "unix:///synthetic/engine.sock"} {
			if err := ValidateProfile(Alpha, engine, socket); err != nil {
				t.Fatal(err)
			}
		}
		for _, socket := range []string{"", "relative.sock", "tcp://127.0.0.1:2375", "ssh://host", "/tmp/../engine.sock", "unix://relative", "/tmp/engine.sock/"} {
			if ValidateProfile(Alpha, engine, socket) == nil {
				t.Fatalf("Alpha accepted %q", socket)
			}
			if err := ValidateProfile(Legacy, engine, socket); err != nil {
				t.Fatalf("legacy compatibility changed: %v", err)
			}
		}
	}
	if ValidateProfile("alph", "docker", "/tmp/engine.sock") == nil {
		t.Fatal("unknown profile accepted")
	}
	if ValidateProfile(Alpha, "unknown", "/tmp/engine.sock") == nil {
		t.Fatal("unknown engine accepted")
	}
	id := Configured(Alpha, "docker", "/synthetic/selected.sock")
	if id.Socket != "unix:///synthetic/selected.sock" || id.Profile != Alpha || id.Status != "Configured only; target negotiation and readiness not established" {
		t.Fatalf("fabricated identity: %+v", id)
	}
}
