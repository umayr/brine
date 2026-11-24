package api

import (
	"strings"
	"testing"
)

func TestDiagnosticOutputBoundedDrain(t *testing.T) {
	var b diagnosticOutput
	chunk := strings.Repeat("x", 4096)
	for i := 0; i < 100; i++ {
		n, err := b.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	if b.buf.Len() != 64<<10 || !strings.HasSuffix(b.String(), "[diagnostic output truncated]") {
		t.Fatal("unbounded or silent truncation")
	}
}
