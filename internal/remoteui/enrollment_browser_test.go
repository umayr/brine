package remoteui

import (
	"os"
	"testing"
)

// TestEnrollmentBrowserFixture renders deterministic synthetic old/new data;
// browser tests using it do not exercise auth, transport or registry commits.
func TestEnrollmentBrowserFixture(t *testing.T) {
	path := os.Getenv("BRINE_ENROLLMENT_FIXTURE")
	if path == "" {
		t.Skip("browser fixture only")
	}
	prior := enrollmentRecord(t)
	candidate := prior
	candidate.Revision++
	candidate.Host = "updated.example"
	prior.DaemonID = "daemon x"
	candidate.DaemonID = "daemon  x"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = enrollmentView.Execute(f, enrollmentPage{CSRF: "fixture-csrf", Token: "fixture-token", Previous: displayEnrollmentFields(prior), Candidate: displayEnrollmentFields(candidate)}); err != nil {
		t.Fatal(err)
	}
}
