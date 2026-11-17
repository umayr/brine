package targetregistry

import "testing"

func TestBindingOwnsPublicKey(t *testing.T) {
	r := record()
	a, err := r.Binding()
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Binding()
	if err != nil {
		t.Fatal(err)
	}
	a.AgentPublicKey[0] ^= 0xff
	if a.AgentPublicKey[0] == b.AgentPublicKey[0] {
		t.Fatal("shared key storage")
	}
	c, err := r.Binding()
	if err != nil || c.AgentPublicKey[0] != b.AgentPublicKey[0] {
		t.Fatal("record mutated", err)
	}
}
