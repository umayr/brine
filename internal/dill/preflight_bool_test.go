package dill

import (
	"flag"
	"io"
	"testing"
)

// Expected accepted spellings are literal Go flag compatibility vectors, checked
// independently against flag.FlagSet rather than the production normalization.
func TestPreflightGoFlagBooleanCompatibility(t *testing.T) {
	spellings := []struct {
		raw  string
		want bool
	}{
		{"1", true}, {"t", true}, {"T", true}, {"TRUE", true}, {"true", true}, {"True", true},
		{"0", false}, {"f", false}, {"F", false}, {"FALSE", false}, {"false", false}, {"False", false},
	}
	a, e := BuildPreflightArguments(preflightArgsBase())
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range spellings {
		t.Run(s.raw, func(t *testing.T) {
			fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			got := fs.Bool("quiet", false, "")
			if e := fs.Parse([]string{"--quiet=" + s.raw}); e != nil || *got != s.want {
				t.Fatal("invalid Go flag vector", e)
			}
			for _, name := range []string{"quiet", "force-recreate"} {
				b := append(append([]string{}, a...), "--"+name+"="+s.raw)
				if ValidatePreflightArguments(b) != nil {
					t.Fatalf("rejected Go flag spelling for %s", name)
				}
			}
			b := append([]string{}, a...)
			for i := range b {
				if b[i] == "--no-prune" {
					b[i] += "=" + s.raw
				}
			}
			// Spelling acceptance does not weaken the required effective no-prune.
			if (ValidatePreflightArguments(b) == nil) != s.want {
				t.Fatal("no-prune semantic normalization")
			}
		})
	}
}

func TestPreflightGoFlagBooleanRejections(t *testing.T) {
	a, e := BuildPreflightArguments(preflightArgsBase())
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"yes", "no", "TrUe", "FaLsE", "", "2", "-1", " true", "false "} {
		fs := flag.NewFlagSet("synthetic", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.Bool("quiet", false, "")
		if fs.Parse([]string{"--quiet=" + s}) == nil {
			t.Fatal("negative Go flag vector")
		}
		for _, name := range []string{"quiet", "force-recreate"} {
			b := append(append([]string{}, a...), "--"+name+"="+s)
			if ValidatePreflightArguments(b) != ErrPreflightArguments {
				t.Fatal("accepted non-Go boolean")
			}
		}
	}
	for _, value := range []string{"true", "false", "1", "0", "T", "F"} {
		if ValidatePreflightArguments(append(append([]string{}, a...), "--quiet", value)) != ErrPreflightArguments {
			t.Fatal("accepted separate bool operand")
		}
		for _, denied := range []string{"dry-run", "yes", "no-deps", "alpha-disposable"} {
			if ValidatePreflightArguments(append(append([]string{}, a...), "--"+denied+"="+value)) != ErrPreflightArguments {
				t.Fatal("accepted denied flag")
			}
		}
	}
	start := preflightArgsBase()
	start.Operation = "start"
	b, e := BuildPreflightArguments(start)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"force-recreate", "no-prune"} {
		for _, value := range []string{"false", "False", "FALSE", "f", "F", "0"} {
			if ValidatePreflightArguments(append(append([]string{}, b...), "--"+name+"="+value)) != ErrPreflightArguments {
				t.Fatal("accepted Up-only false flag")
			}
		}
	}
}
