package config

import "testing"

func TestHistoryOnlyOptInAuthenticatedQualifiedAlpha(t *testing.T) {
	c := Default()
	if e := c.validateOperationHistory(); e != nil {
		t.Fatal("legacy changed outside opt-in", e)
	}
	c.OperationStateDir = "/synthetic/history"
	if e := c.validateOperationHistory(); e == nil {
		t.Fatal("legacy history accepted")
	}
	c.Profile = "alpha"
	if e := c.validateOperationHistory(); e == nil {
		t.Fatal("unqualified history accepted")
	}
	c.CoordinationStateDir = "/synthetic/coord"
	c.CoordinationLayout = "native-local-docker-v1"
	c.CoordinationDaemonID = "independent-daemon"
	c.NoAuth = true
	if e := c.validateOperationHistory(); e == nil {
		t.Fatal("noauth history accepted")
	}
	c.NoAuth = false
	c.DataDir = "/synthetic/history/data"
	if e := c.validateOperationHistory(); e == nil {
		t.Fatal("history ancestor of editable data accepted")
	}
	c.DataDir = "/synthetic/data"
	c.OperationStateDir = "/synthetic/coord/history"
	if e := c.validateOperationHistory(); e == nil {
		t.Fatal("nested coordination/history accepted")
	}
}
