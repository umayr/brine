package dill

import (
	"strings"
	"testing"
)

func TestSecretCapabilityOutputBound(t *testing.T) {
	for _, size := range []int{8192, 8193, 20000} {
		var out boundedCapabilityOutput
		n, err := out.Write([]byte(strings.Repeat("x", size)))
		if size <= 8192 {
			if err != nil || n != size {
				t.Fatalf("size %d: n=%d err=%v", size, n, err)
			}
		} else if err == nil || n != 0 {
			t.Fatalf("size %d: n=%d err=%v", size, n, err)
		}
	}
}
