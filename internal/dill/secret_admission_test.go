package dill

import (
	"context"
	"errors"
	"github.com/umayr/brine/internal/secretbindings"
	"testing"
)

func TestSecretAdmissionReturnsQuarantineDenial(t *testing.T) {
	denied := errors.New("synthetic quarantine")
	c := &secretbindings.Coordinator{}
	c.CheckAdmission = func(string) error { return denied }
	s := &SecretRunner{Coordinator: c}
	for i := 0; i < 2; i++ {
		unlock, err := s.lockAdmission(context.Background(), "synthetic")
		if !errors.Is(err, denied) || unlock != nil {
			t.Fatalf("admission: %v %v", unlock != nil, err)
		}
	}
	c.CheckAdmission = nil
	unlock, err := s.lockAdmission(context.Background(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
