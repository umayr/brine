package web

import (
	"strings"
	"testing"
)

func TestSourceLineDiffDirectionAndBounds(t *testing.T) {
	lines, unavailable := sourceLines([]byte("keep\nold\nno-newline"), []byte("keep\nnew\nno-newline\n"))
	if unavailable != "" || len(lines) != 5 || lines[0].Kind != "same" || lines[1].Kind != "remove" || lines[1].Text != "old" || !lines[2].Unterminated || lines[3].Kind != "add" || lines[3].Text != "new" || lines[4].Unterminated {
		t.Fatalf("unexpected comparison: %+v (%s)", lines, unavailable)
	}
	for _, c := range [][]byte{[]byte(strings.Repeat("x", sourceDiffBytes+1)), []byte(strings.Repeat("x\n", sourceDiffLines+1)), {0xff}, {0}} {
		if diff, message := sourceLines(c, nil); len(diff) > 0 || message == "" {
			t.Fatal("unavailable comparison presented as a diff")
		}
	}
}
