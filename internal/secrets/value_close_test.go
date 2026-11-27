package secrets

import (
	"sync"
	"testing"
)

func TestValueClose(t *testing.T) {
	v, err := NewValue([]byte("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	copy := v
	bytes := copy.Bytes()
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				copy.Bytes()
				v.Close()
			}
		}()
	}
	wg.Wait()
	copy.Close()
	if len(v.Bytes()) != 0 || len(copy.Bytes()) != 0 {
		t.Fatal("shared value remained readable")
	}
	if string(bytes) != "synthetic" {
		t.Fatal("close changed caller-owned copy")
	}
	clear(bytes)
}
