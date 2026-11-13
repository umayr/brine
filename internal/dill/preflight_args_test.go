package dill

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func preflightArgsBase() PreflightArguments {
	return PreflightArguments{Operation: "up", ConfigFile: "/private/synthetic/compose.pkl", Socket: "/run/synthetic-only.sock", Stack: "synthetic", Coordination: QualifiedOperationConfig{StateDir: "/private/synthetic/coord", Layout: "native-local-docker-v1", DaemonID: "synthetic-not-a-real-daemon"}}
}
func preflightArgsReplace(a []string, key, value string) []string {
	b := append([]string{}, a...)
	for i := range b {
		if b[i] == key {
			b[i+1] = value
			return b
		}
	}
	return append(b, key, value)
}
func TestPreflightArgumentsGolden(t *testing.T) {
	b, e := os.ReadFile("testdata/preflight-v1/argv.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Positive      []struct{ Argv []string }
		BindingSuffix []string
		NegativeCases []string
	}
	if json.Unmarshal(b, &f) != nil || len(f.Positive) != 5 {
		t.Fatal("fixture")
	}
	for _, p := range f.Positive {
		c := preflightArgsBase()
		c.Operation = p.Argv[1]
		a, e := BuildPreflightArguments(c)
		if e != nil || !reflect.DeepEqual(a, p.Argv) {
			t.Fatal("canonical preview", e)
		}
		bound, e := BuildBoundPreflightArguments(c, f.BindingSuffix[3])
		if e != nil {
			t.Fatal(e)
		}
		expected := append([]string{}, a...)
		expected[0] = "operation"
		expected[3] = OperationVersion
		expected = append(expected, f.BindingSuffix...)
		if !reflect.DeepEqual(bound, expected) || bound[len(bound)-1] != f.BindingSuffix[3] {
			t.Fatal("canonical bound, verbatim digest")
		}
	}
	// All closed denied cases named by the shared fixture are mechanically tested.
	a, _ := BuildPreflightArguments(preflightArgsBase())
	bound, _ := BuildBoundPreflightArguments(preflightArgsBase(), f.BindingSuffix[3])
	mutations := map[string][]string{
		"missing_or_empty_binding_half":      bound[:len(bound)-2],
		"bad_or_double_prefixed_hash":        preflightArgsReplace(bound, "--expect-preflight", "sha256:"+f.BindingSuffix[3]),
		"unknown_version":                    preflightArgsReplace(bound, "--preflight-contract", "dill.preflight/2"),
		"binding_without_full_qualification": preflightArgsReplace(bound, "--coordination-layout", ""),
		"missing_explicit_f":                 preflightArgsReplace(a, "-f", ""),
		"duplicate_flag_or_stack_alias":      append(append([]string{}, a...), "--stack-name", "synthetic"),
		"service_operand":                    append(append([]string{}, a...), "web"),
		"no_deps":                            append(append([]string{}, a...), "--no-deps=false"),
		"dry_run_true_or_false_explicit":     append(append([]string{}, a...), "--dry-run=false"),
		"yes_y":                              append(append([]string{}, a...), "-y"),
		"force_teardown":                     append(append([]string{}, a...), "--force"),
		"alpha_disposable":                   append(append([]string{}, a...), "--alpha-disposable"),
		"env_file_secret_proof":              append(append([]string{}, a...), "--env-file", "/private/sentinel"),
		"up_only_flags_on_other_actions":     append([]string{"preflight", "start"}, a[2:]...),
		"pull_always_or_missing":             preflightArgsReplace(a, "--pull", "always"),
		"bare_separator":                     append(append([]string{}, a...), "--"),
		"unknown_flag":                       append(append([]string{}, a...), "--unknown"),
		"default_socket_or_ambient_target":   preflightArgsReplace(a, "--engine-socket", ""),
		"unsupported_engine_layout_os":       preflightArgsReplace(a, "--engine", "podman"),
		"negative_zero_or_invalid_timeout":   preflightArgsReplace(a, "--timeout", "0"),
	}
	for _, name := range f.NegativeCases {
		t.Run(name, func(t *testing.T) {
			if ValidatePreflightArguments(a) != nil || ValidatePreflightArguments(bound) != nil {
				t.Fatal("invalid baseline")
			}
			bad, ok := mutations[name]
			if !ok {
				t.Fatal("missing required case")
			}
			if ValidatePreflightArguments(bad) != ErrPreflightArguments {
				t.Fatal("accepted invalid structural argv")
			}
		})
	}
}
func TestPreflightArgumentsGrammar(t *testing.T) {
	base := preflightArgsBase()
	a, _ := BuildPreflightArguments(base)
	for _, alias := range []string{"-p", "--stack-name", "-project-name"} {
		b := append([]string{}, a...)
		for i := range b {
			if b[i] == "-p" {
				b[i] = alias
			}
		}
		if ValidatePreflightArguments(b) != nil {
			t.Fatal("stack alias")
		}
	}
	// Single dash and equals values, quiet, force-recreate are valid grammar.
	b := []string{a[0], a[1]}
	for i := 2; i < len(a); i++ {
		key := strings.TrimLeft(a[i], "-")
		if key == "no-prune" {
			b = append(b, "-no-prune=true")
			continue
		}
		b = append(b, "-"+key+"="+a[i+1])
		i++
	}
	b = append(b, "--quiet=false", "--force-recreate=true")
	if ValidatePreflightArguments(b) != nil {
		t.Fatal("equals grammar")
	}
	// Flag-looking text in a value is not interpreted as another flag.
	for _, value := range []string{"/private/--dry-run/compose.pkl", "--env-file=literal-value", "a b"} {
		if ValidatePreflightArguments(preflightArgsReplace(a, "-f", value)) != nil {
			t.Fatal("scanned argument value")
		}
	}
	for _, key := range []string{"-f", "--engine", "--engine-socket", "-p", "--timeout", "--coordination-layout", "--coordination-daemon-id", "--coordination-state-dir", "--coordination-source", "--contract", "--format"} {
		for i := 2; i < len(a); i++ {
			if a[i] != key {
				continue
			}
			bad := append(append([]string{}, a[:i]...), a[i+2:]...)
			if ValidatePreflightArguments(bad) == nil {
				t.Fatal("implicit field", key)
			}
		}
		bad := append(append([]string{}, a...), key, "sentinel")
		if ValidatePreflightArguments(bad) == nil {
			t.Fatal("duplicate field", key)
		}
	}
	for _, key := range []string{"--dry-run", "--yes", "-y", "--no-deps", "--force", "--alpha-disposable", "--secret-proof", "--source-snapshot-digest", "--env-file", "--rm", "--teardown"} {
		for _, suffix := range []string{"", "=false", "=true"} {
			bad := append(append([]string{}, a...), key+suffix)
			if ValidatePreflightArguments(bad) == nil {
				t.Fatal("denied", key)
			}
		}
	}
	for _, timeout := range []string{"0", "-1s", "nonsense", "999999999999999999999999h"} {
		if ValidatePreflightArguments(preflightArgsReplace(a, "--timeout", timeout)) == nil {
			t.Fatal("timeout")
		}
	}
	for _, op := range []string{"rm", "teardown", "plan", "", "up web"} {
		c := base
		c.Operation = op
		if _, e := BuildPreflightArguments(c); e == nil {
			t.Fatal("operation")
		}
	}
	for _, digest := range []string{"", "sha256:abc", strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:sha256:" + strings.Repeat("a", 64)} {
		if _, e := BuildBoundPreflightArguments(base, digest); e == nil {
			t.Fatal("digest")
		}
	}
	c := base
	c.Operation = "start"
	c.ForceRecreate = true
	if _, e := BuildPreflightArguments(c); e == nil {
		t.Fatal("up flag")
	}
	c = base
	c.ConfigFile = "\x00sentinel"
	if _, e := BuildPreflightArguments(c); e == nil {
		t.Fatal("NUL")
	}
}
