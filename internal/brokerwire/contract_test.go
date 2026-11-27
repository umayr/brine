package brokerwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func brokerScope() Scope {
	d := "sha256:" + strings.Repeat("a", 64)
	return Scope{Registration: strings.Repeat("1", 32), Stack: "app", Engine: "docker", TargetDigest: d, EndpointDigest: d, SourceDigest: d, ConfigDigest: d, PolicyRevision: d, Generation: strings.Repeat("2", 32), BindingRevision: 1}
}
func TestBrokerWireExplicitOnlyFrozenBounded(t *testing.T) {
	item, err := NewItem("password", []byte("synthetic-sensitive"))
	if err != nil {
		t.Fatal(err)
	}
	e := Envelope{Scope: brokerScope(), Items: []Item{item}}
	defer e.Close()
	if raw, err := json.Marshal(e); err == nil || len(raw) != 0 {
		t.Fatal("implicit value serialization")
	}
	if strings.Contains(fmt.Sprintf("%#v %+v", e, item), "synthetic-sensitive") {
		t.Fatal("printable values")
	}
	raw, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	decoded, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	if decoded.Scope != e.Scope || string(decoded.Items[0].Bytes()) != "synthetic-sensitive" {
		t.Fatal("wire mismatch")
	}
	changed := bytes.Replace(raw, []byte(`"contract":`), []byte(`"unknown":"private","contract":`), 1)
	if _, err = Decode(bytes.NewReader(changed)); err == nil {
		t.Fatal("unknown wire field accepted")
	}
}
