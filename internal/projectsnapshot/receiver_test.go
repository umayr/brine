package projectsnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func receiverFile(p string, b []byte) File {
	return File{Path: p, Content: b, SHA256: sha256.Sum256(b)}
}
func TestProvisionalReceiverParityFixtures(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		entry string
		ok    bool
	}{
		{"normal", []string{"dir/main.pkl", "dir/other"}, "dir/main.pkl", true},
		{"duplicate", []string{"x", "x"}, "x", false}, {"case duplicate", []string{"x", "X"}, "x", false},
		{"ancestor", []string{"x", "x/y"}, "x", false}, {"reverse ancestor", []string{"x/y", "X"}, "X", false},
		{"directory spelling", []string{"dir/x", "Dir/y"}, "dir/x", false}, {"missing entry", []string{"x"}, "X", false},
	}
	for _, p := range []string{"", "../x", "/x", "a//b", "a/./b", `C:/x`, `a\b`, ".GIT/x", "NUL.txt", "com1.foo", "x.", "x ", "a?b", "é", "a\x7f", "a\x1f", "PRN", "AUX", "CON", "LPT9.txt", "a<b", "a>b", `a"b`, "a|b", "a*b"} {
		cases = append(cases, struct {
			name  string
			paths []string
			entry string
			ok    bool
		}{fmt.Sprintf("invalid %q", p), []string{p}, p, false})
	}
	for _, p := range []string{"COM0", "COM10", "LPT0", "NULx", "a b", "a.git", "~"} {
		cases = append(cases, struct {
			name  string
			paths []string
			entry string
			ok    bool
		}{"valid " + p, []string{p}, p, true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Snapshot{Entrypoint: tc.entry}
			for _, p := range tc.paths {
				s.Files = append(s.Files, receiverFile(p, []byte("secret")))
			}
			err := ValidateProvisionalReceiver(context.Background(), s)
			if (err == nil) != tc.ok {
				t.Fatalf("got %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("content leaked")
			}
		})
	}
	s := Snapshot{Entrypoint: "x", Files: []File{{Path: "x", Content: []byte("bad")}}}
	if ValidateProvisionalReceiver(context.Background(), s) == nil {
		t.Fatal("digest accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ValidateProvisionalReceiver(ctx, s) != context.Canceled {
		t.Fatal("cancel ignored")
	}
}
func TestProvisionalWireEncoding(t *testing.T) {
	f := receiverFile("a&b", []byte("secret"))
	s := Snapshot{Entrypoint: f.Path, Files: []File{f}}
	wire := struct {
		Schema     string `json:"schema"`
		Entrypoint string `json:"entrypoint"`
		Files      []struct {
			Path   string `json:"path"`
			Bytes  []byte `json:"bytes"`
			Length int    `json:"length"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}{Schema: "dill.snapshot.v1", Entrypoint: s.Entrypoint}
	wire.Files = append(wire.Files, struct {
		Path   string `json:"path"`
		Bytes  []byte `json:"bytes"`
		Length int    `json:"length"`
		SHA256 string `json:"sha256"`
	}{f.Path, f.Content, len(f.Content), hex.EncodeToString(f.SHA256[:])})
	encoded, err := json.Marshal(wire)
	if err != nil || provisionalWireSize(s) != int64(len(encoded)) {
		t.Fatalf("wire size mismatch: %d vs %d (%v)", provisionalWireSize(s), len(encoded), err)
	}
	// Exercise exact body cap accounting separately: stage decoded limits make
	// such a large body unreachable for otherwise valid snapshots today.
	data := make([]byte, 18<<20)
	seen := map[int64]bool{}
	for n := len(data) - 200; n <= len(data); n++ {
		for p := 1; p <= 255; p++ {
			candidate := Snapshot{Entrypoint: "x", Files: []File{{Path: strings.Repeat("a", p), Content: data[:n]}}}
			size := provisionalWireSize(candidate)
			if size == 24<<20 || size == 24<<20+1 {
				seen[size] = true
			}
		}
	}
	if !seen[24<<20] || !seen[24<<20+1] {
		t.Fatal("did not exercise both wire cap boundaries")
	}
}

func TestProvisionalReceiverBoundaries(t *testing.T) {
	check := func(t *testing.T, s Snapshot, ok bool) {
		t.Helper()
		if err := ValidateProvisionalReceiver(context.Background(), s); (err == nil) != ok {
			t.Fatalf("got %v, want accepted %v", err, ok)
		}
	}
	for _, n := range []int{1000, 1001} {
		t.Run(fmt.Sprint("files", n), func(t *testing.T) {
			s := Snapshot{Entrypoint: "f0"}
			for i := 0; i < n; i++ {
				s.Files = append(s.Files, receiverFile(fmt.Sprint("f", i), nil))
			}
			check(t, s, n == 1000)
		})
	}
	for _, n := range []int{254, 255} {
		t.Run(fmt.Sprint("directories", n), func(t *testing.T) {
			s := Snapshot{Entrypoint: "d0/f"}
			for i := 0; i < n; i++ {
				s.Files = append(s.Files, receiverFile(fmt.Sprintf("d%d/f", i), nil))
			}
			check(t, s, n == 254)
		})
	}
	for _, n := range []int{32, 33} {
		p := strings.Repeat("a/", n-1) + "f"
		t.Run(fmt.Sprint("depth", n), func(t *testing.T) { check(t, Snapshot{p, []File{receiverFile(p, nil)}}, n == 32) })
	}
	for _, n := range []int{255, 256} {
		p := strings.Repeat("a", n)
		t.Run(fmt.Sprint("component", n), func(t *testing.T) { check(t, Snapshot{p, []File{receiverFile(p, nil)}}, n == 255) })
	}
	for _, n := range []int{512, 513} {
		p := strings.Repeat("a", 255) + "/" + strings.Repeat("b", n-258) + "/x"
		t.Run(fmt.Sprint("path", n), func(t *testing.T) { check(t, Snapshot{p, []File{receiverFile(p, nil)}}, n == 512) })
	}
	for _, n := range []int{4 << 20, 4<<20 + 1} {
		t.Run(fmt.Sprint("filebytes", n), func(t *testing.T) {
			f := receiverFile("x", make([]byte, n))
			check(t, Snapshot{"x", []File{f}}, n == 4<<20)
		})
	}
	t.Run("total", func(t *testing.T) {
		b := make([]byte, 4<<20)
		s := Snapshot{Entrypoint: "a"}
		for _, p := range []string{"a", "b", "c", "d"} {
			s.Files = append(s.Files, receiverFile(p, b))
		}
		check(t, s, true)
		s.Files = append(s.Files, receiverFile("e", []byte{1}))
		check(t, s, false)
	})
}
