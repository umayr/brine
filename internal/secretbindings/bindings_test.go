package secretbindings

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const validDoc = `{"version":"dill.secret-bindings/1","target":"lab-a","stack":"demo","bindings":[{"name":"db-password","source":"db-prod","revision":"r2"}]}`

func TestParseStrictReferences(t *testing.T) {
	if _, err := Parse([]byte(validDoc), "demo", "lab-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(validDoc), "demo", "lab-b"); err == nil {
		t.Fatal("cross-target accepted")
	}
	if _, err := Parse([]byte(validDoc), "other", "lab-a"); err == nil {
		t.Fatal("cross-stack accepted")
	}
	cases := []string{
		strings.Replace(validDoc, `"revision":"r2"`, `"revision":null`, 1),
		strings.Replace(validDoc, `"revision":"r2"`, `"revision":"r2","revision":"r3"`, 1),
		strings.Replace(validDoc, `"revision":"r2"`, `"revision":"r2","path":"/tmp/value"`, 1),
		strings.Replace(validDoc, `"revision":"r2"`, `"revision":"r2","value":"synthetic-plaintext-canary"`, 1),
		strings.Replace(validDoc, `"bindings":[`, `"bindings":null,"x":[`, 1),
		strings.Replace(validDoc, `"stack":"demo"`, `"stack":"other"`, 1),
		strings.Replace(validDoc, `"source":"db-prod"`, `"source":"../../source"`, 1),
		validDoc + ` {}`, validDoc + "\xff", strings.Repeat(" ", MaxDocument+1),
		strings.Replace(validDoc, `}]}`, `},{"name":"db-password","source":"s","revision":"r"}]}`, 1),
	}
	for i, s := range cases {
		if _, err := Parse([]byte(s), "demo", "lab-a"); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	entries := make([]Binding, 257)
	for i := range entries {
		entries[i] = Binding{Name: fmt.Sprintf("n%d", i), Source: "s", Revision: "r"}
	}
	b, _ := json.Marshal(Document{Version: Version, Stack: "demo", Target: "lab-a", Bindings: entries})
	if _, err := Parse(b, "demo", "lab-a"); err == nil {
		t.Fatal("accepted too many bindings")
	}
}
