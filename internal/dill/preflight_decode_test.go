package dill

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type preflightFixture struct {
	ExitCode int             `json:"exitCode"`
	Stdout   json.RawMessage `json:"stdout"`
	Runtime  string          `json:"runtime"`
}

func preflightFixtureRead(t *testing.T, name string) preflightFixture {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("testdata/preflight-v1", name))
	if e != nil {
		t.Fatal(e)
	}
	var f preflightFixture
	if json.Unmarshal(b, &f) != nil || f.Runtime != "synthetic-design-not-executed" {
		t.Fatal("bad fixture")
	}
	return f
}
func preflightBase(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if json.Unmarshal(preflightFixtureRead(t, "preview-up-create.json").Stdout, &m) != nil {
		t.Fatal("base")
	}
	return m
}
func preflightJSON(t *testing.T, m any) []byte {
	t.Helper()
	b, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func preflightReject(t *testing.T, b []byte) {
	t.Helper()
	// Every rejection is anchored by an independently accepted valid baseline.
	if _, e := DecodePreflightResult(preflightFixtureRead(t, "preview-up-create.json").Stdout); e != nil {
		t.Fatal("invalid baseline", e)
	}
	if r, e := DecodePreflightResult(b); e != ErrPreflightContract || r != nil {
		t.Fatal("accepted malformed evidence")
	}
	n := 0
	v := ObservePreflightInvocation(b, &n)
	if v.Result != nil || v.Status != "unknown" {
		t.Fatal("promoted malformed delivery")
	}
}
func preflightRows(m map[string]any) []any { return m["resources"].([]any) }
func preflightRow(m map[string]any, i int) map[string]any {
	return preflightRows(m)[i].(map[string]any)
}
func preflightScope(m map[string]any, k string) map[string]any {
	return m["coverage"].(map[string]any)[k].(map[string]any)
}

func TestPreflightGolden(t *testing.T) {
	files, e := filepath.Glob("testdata/preflight-v1/preview-*.json")
	if e != nil || len(files) != 8 {
		t.Fatal("fixture list")
	}
	for _, p := range files {
		t.Run(filepath.Base(p), func(t *testing.T) {
			f := preflightFixtureRead(t, filepath.Base(p))
			r, e := DecodePreflightResult(f.Stdout)
			if e != nil {
				t.Fatal(e)
			}
			v := ObservePreflightInvocation(f.Stdout, &f.ExitCode)
			if v.Result == nil || v.Result.Outcome != r.Outcome {
				t.Fatal("golden invocation")
			}
			round, e := DecodePreflightInvocation(preflightJSON(t, v))
			if e != nil || !reflect.DeepEqual(round, &v) {
				t.Fatal("round trip", e)
			}
			if r.Outcome == "succeeded" {
				if CheckPreflightBinding(&v, PreflightBinding{r.Stack, r.Operation, "docker", r.Target.EndpointDigest, r.Target.QualifiedIdentityDigest}) != nil {
					t.Fatal("binding")
				}
			} else if v.Status == "succeeded" || CheckPreflightBinding(&v, PreflightBinding{}) == nil {
				t.Fatal("failure confirmable")
			}
		})
	}
	for _, name := range []string{"capabilities-supported.json", "capabilities-unsupported.json"} {
		f := preflightFixtureRead(t, name)
		c, e := DecodePreflightCapabilities(f.Stdout)
		if e != nil || c.Supported != (name == "capabilities-supported.json") {
			t.Fatal(name, e)
		}
	}
	// Negotiated apply codes must NOT silently widen operation/1 globally.
	f := preflightFixtureRead(t, "apply-mismatch.json")
	old := map[string]any{"receipt": json.RawMessage(f.Stdout), "exitCode": f.ExitCode, "status": "failed"}
	if _, e := DecodeOperationInvocation(preflightJSON(t, old)); e == nil {
		t.Fatal("legacy operation parser widened")
	}
}

func TestPreflightRequiredRejects(t *testing.T) {
	changes := map[string]func(map[string]any){
		"unknown_version":                        func(m map[string]any) { m["version"] = "dill.preflight/2" },
		"unknown_top_or_nested_key":              func(m map[string]any) { preflightRow(m, 0)["private-sentinel"] = "/private/sentinel" },
		"null_required_boolean":                  func(m map[string]any) { m["resourcesTruncated"] = nil },
		"missing_resources_or_errors_array":      func(m map[string]any) { delete(m, "errors") },
		"missing_coverage_scope":                 func(m map[string]any) { delete(preflightScope(m, "network"), "scope") },
		"fractional_negative_or_string_count":    func(m map[string]any) { preflightScope(m, "network")["count"] = 1.5 },
		"coverage_row_or_present_count_mismatch": func(m map[string]any) { preflightScope(m, "containers")["presentCount"] = 1 },
		"duplicate_kind_name": func(m map[string]any) {
			m["resources"] = append(preflightRows(m), preflightRow(m, 0))
			preflightScope(m, "containers")["count"] = 2
		},
		"short_container_or_network_id": func(m map[string]any) {
			x := preflightRow(m, 1)
			x["presence"] = "present"
			x["identityKind"] = "immutable-id"
			x["identity"] = strings.Repeat("a", 12)
			preflightScope(m, "network")["presentCount"] = 1
		},
		"mismatched_identityKind_presence": func(m map[string]any) { preflightRow(m, 0)["identityKind"] = "immutable-id" },
		"absent_start_container": func(m map[string]any) {
			m["operation"] = "start"
			preflightRow(m, 0)["action"] = "start"
			preflightRow(m, 2)["action"] = "retain"
		},
		"network_absent_retain_mislabeled_create": func(m map[string]any) { preflightRow(m, 1)["action"] = "create" },
		"recreate_fake_future_id": func(m map[string]any) {
			x := preflightRow(m, 0)
			x["action"] = "recreate"
			x["identity"] = "future-container"
		},
		"container_retention_preserve": func(m map[string]any) { preflightRow(m, 0)["retention"] = "preserve" },
		"volume_immutable_incarnation_claim": func(m map[string]any) {
			x := preflightRow(m, 2)
			x["presence"] = "present"
			x["identityKind"] = "immutable-id"
			x["identity"] = strings.Repeat("a", 64)
			preflightScope(m, "volumes")["presentCount"] = 1
		},
		"nonnull_target_missing_empty_or_selector_only_qualifiedDigest": func(m map[string]any) { delete(m["target"].(map[string]any), "qualifiedIdentityDigest") },
		"success_missing_digest":                      func(m map[string]any) { m["inputDigest"] = nil },
		"failure_rows_or_digest_present":              func(m map[string]any) { m["outcome"] = "failed" },
		"truncated_success":                           func(m map[string]any) { m["resourcesTruncated"] = true },
		"metadata_unknown_success":                    func(m map[string]any) { m["coordinationEffects"] = "unknown" },
		"unsafe_name_control_invalid_utf8_or_over255": func(m map[string]any) { preflightRow(m, 0)["name"] = strings.Repeat("x", 256) },
		"field_count_array_null_instead_empty":        func(m map[string]any) { m["errors"] = nil },
	}
	special := map[string]bool{"duplicate_json_key": true, "trailing_json": true, "exit1_success_marker": true, "oversized_bytes": true}
	b, e := os.ReadFile("testdata/preflight-v1/negative-wire-cases.json")
	if e != nil {
		t.Fatal(e)
	}
	var list struct {
		RequiredRejects []string `json:"requiredRejects"`
	}
	if json.Unmarshal(b, &list) != nil {
		t.Fatal("list")
	}
	for _, name := range list.RequiredRejects {
		t.Run(name, func(t *testing.T) {
			m := preflightBase(t)
			if change, ok := changes[name]; ok {
				change(m)
				preflightReject(t, preflightJSON(t, m))
				return
			}
			if !special[name] {
				t.Fatal("unimplemented required rejection")
			}
			b := preflightJSON(t, m)
			switch name {
			case "duplicate_json_key":
				b = append([]byte(`{"version":"dill.preflight/1",`), b[1:]...)
			case "trailing_json":
				b = append(b, []byte(`{}`)...)
			case "oversized_bytes":
				b = append(b, bytes.Repeat([]byte(" "), PreflightMaxOutputBytes)...)
			case "exit1_success_marker":
				n := 1
				if ObservePreflightInvocation(b, &n).Result != nil {
					t.Fatal("exit mismatch promoted")
				}
				return
			}
			preflightReject(t, b)
		})
	}
}

// Exercise every field recursively: omission, null, duplicate and unknown keys.
func TestPreflightClosedRecursiveObjects(t *testing.T) {
	var walk func(map[string]any, []any)
	get := func(m map[string]any, path []any) map[string]any {
		var cur any = m
		for _, p := range path {
			switch k := p.(type) {
			case string:
				cur = cur.(map[string]any)[k]
			case int:
				cur = cur.([]any)[k]
			}
		}
		return cur.(map[string]any)
	}
	walk = func(object map[string]any, path []any) {
		keys := make([]string, 0, len(object))
		for k := range object {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Run(fmt.Sprint(path)+"/"+k, func(t *testing.T) {
				m := preflightBase(t)
				delete(get(m, path), k)
				preflightReject(t, preflightJSON(t, m))
				if len(path) != 0 || !oneOf(k, "target", "inputDigest", "selectionDigest") {
					m = preflightBase(t)
					get(m, path)[k] = nil
					preflightReject(t, preflightJSON(t, m))
				}
				// Duplicate even equal values at precisely this object.
				m = preflightBase(t)
				raw := preflightJSON(t, get(m, path))
				val := preflightJSON(t, get(m, path)[k])
				prefix := []byte(`{"` + k + `":` + string(val) + `,`)
				dup := append(prefix, raw[1:]...)
				whole := preflightJSON(t, m)
				whole = bytes.Replace(whole, raw, dup, 1)
				preflightReject(t, whole)
			})
			p := append(append([]any{}, path...), k)
			switch v := object[k].(type) {
			case map[string]any:
				walk(v, p)
			case []any:
				for i, x := range v {
					if child, ok := x.(map[string]any); ok {
						walk(child, append(append([]any{}, p...), i))
					}
				}
			}
		}
		m := preflightBase(t)
		get(m, path)["sentinel-unrecognized"] = "/private/sentinel"
		preflightReject(t, preflightJSON(t, m))
	}
	walk(preflightBase(t), nil)
	// Error objects need their own nonempty failure baseline.
	f := preflightFixtureRead(t, "preview-limit-failure.json")
	var m map[string]any
	_ = json.Unmarshal(f.Stdout, &m)
	for _, key := range []string{"code", "message"} {
		var clone map[string]any
		_ = json.Unmarshal(f.Stdout, &clone)
		delete(clone["errors"].([]any)[0].(map[string]any), key)
		preflightReject(t, preflightJSON(t, clone))
	}
}

func TestPreflightFramingAndLimits(t *testing.T) {
	b := preflightJSON(t, preflightBase(t))
	for _, good := range [][]byte{b, append(append([]byte{}, b...), '\n')} {
		if _, e := DecodePreflightResult(good); e != nil {
			t.Fatal(e)
		}
	}
	for _, bad := range [][]byte{append([]byte(" "), b...), append(append([]byte{}, b...), ' '), append(append([]byte{}, b...), '\r', '\n'), append(append([]byte{}, b...), '\n', '\n'), append([]byte{0xff}, b...), append(b, []byte("\n{}")...)} {
		preflightReject(t, bad)
	}
	exact := append([]byte{'{'}, bytes.Repeat([]byte(" "), PreflightMaxOutputBytes-len(b))...)
	exact = append(exact, b[1:]...)
	if len(exact) != PreflightMaxOutputBytes {
		t.Fatal("boundary")
	}
	if _, e := DecodePreflightResult(exact); e != nil {
		t.Fatal("exact bytes", e)
	}
	preflightReject(t, append(exact, '\n'))
	for _, name := range []string{"_bad", "-bad", ".bad", "a/b", "a b", "a\n", "é", "x\x00", strings.Repeat("x", 256)} {
		m := preflightBase(t)
		x := preflightRow(m, 0)
		x["name"] = name
		x["identity"] = name
		preflightReject(t, preflightJSON(t, m))
	}
	for _, n := range []int{1, 255} {
		m := preflightBase(t)
		x := preflightRow(m, 0)
		x["name"] = strings.Repeat("x", n)
		x["identity"] = x["name"]
		if _, e := DecodePreflightResult(preflightJSON(t, m)); e != nil {
			t.Fatal("name boundary", e)
		}
	}
	preflightReject(t, bytes.Replace(b, []byte(`"synthetic_web"`), []byte(`"\u0073ynthetic_web"`), 1))
	for _, count := range []int{256, 257} {
		m := preflightBase(t)
		rows := []any{preflightRow(m, 1), preflightRow(m, 2)}
		for i := 0; i < count-2; i++ {
			rows = append(rows, map[string]any{"kind": "container", "name": fmt.Sprintf("c%d", i), "identity": fmt.Sprintf("c%d", i), "identityKind": "planned-name", "presence": "absent", "action": "create", "retention": "not-applicable"})
		}
		m["resources"] = rows
		preflightScope(m, "containers")["count"] = count - 2
		if count == 256 {
			if _, e := DecodePreflightResult(preflightJSON(t, m)); e != nil {
				t.Fatal("row boundary", e)
			}
		} else {
			preflightReject(t, preflightJSON(t, m))
		}
	}
	for _, v := range []any{-1, 257, "1", 1.2, true} {
		m := preflightBase(t)
		preflightScope(m, "network")["count"] = v
		preflightReject(t, preflightJSON(t, m))
	}
}

func TestPreflightIdentityMatrix(t *testing.T) {
	for _, kind := range []string{"container", "network", "volume"} {
		for _, op := range []string{"up", "start", "restart", "stop", "down"} {
			for _, presence := range []string{"present", "absent"} {
				for _, action := range []string{"create", "recreate", "ensure-started", "start", "restart", "stop", "remove", "none", "ensure", "retain", "unknown"} {
					x := PreflightResource{Kind: kind, Name: "safe", Identity: "safe", IdentityKind: "planned-name", Presence: presence, Action: action, Retention: "preserve"}
					if kind == "container" {
						x.Retention = "not-applicable"
					}
					live := presence == "present"
					if live {
						if kind == "volume" {
							x.IdentityKind = "engine-name"
						} else {
							x.IdentityKind = "immutable-id"
							x.Identity = strings.Repeat("a", 64)
						}
					}
					want := false
					switch kind {
					case "container":
						switch op {
						case "up":
							want = (!live && action == "create") || (live && oneOf(action, "recreate", "ensure-started"))
						case "start", "restart":
							want = live && action == op
						case "stop":
							want = (live && action == "stop") || (!live && action == "none")
						case "down":
							want = (live && action == "remove") || (!live && action == "none")
						}
					case "network":
						want = (oneOf(op, "up", "start", "restart") && action == "ensure") || (oneOf(op, "stop", "down") && action == "retain")
					case "volume":
						want = action == "retain" || (op == "up" && action == "ensure")
					}
					if validPreflightResource(x, op) != want {
						t.Fatalf("matrix %s %s %s %s", kind, op, presence, action)
					}
					if want {
						for _, bad := range []string{"", "sha256:" + strings.Repeat("a", 64), strings.Repeat("a", 12), strings.Repeat("A", 64), "future-name"} {
							y := x
							y.Identity = bad
							if validPreflightResource(y, op) {
								t.Fatal("unsafe identity")
							}
						}
						for _, bad := range []string{"unknown", "planned-name", "immutable-id", "engine-name"} {
							if bad != x.IdentityKind {
								y := x
								y.IdentityKind = bad
								if validPreflightResource(y, op) {
									t.Fatal("bad identity kind")
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestPreflightProcessBindingAndFailures(t *testing.T) {
	f := preflightFixtureRead(t, "preview-up-create.json")
	zero := 0
	v := ObservePreflightInvocation(f.Stdout, &zero)
	want := PreflightBinding{"synthetic", "up", "docker", v.Result.Target.EndpointDigest, v.Result.Target.QualifiedIdentityDigest}
	for _, field := range []string{"Stack", "Operation", "Engine", "EndpointDigest", "QualifiedIdentityDigest"} {
		changed := want
		reflect.ValueOf(&changed).Elem().FieldByName(field).SetString("sentinel")
		if CheckPreflightBinding(&v, changed) != ErrPreflightBinding {
			t.Fatal("unbound", field)
		}
	}
	// Correctly formed but independently wrong digests must not bind either.
	changed := want
	changed.QualifiedIdentityDigest = "sha256:" + strings.Repeat("f", 64)
	if CheckPreflightBinding(&v, changed) == nil {
		t.Fatal("qualified mismatch")
	}
	changed = want
	changed.EndpointDigest = "sha256:" + strings.Repeat("f", 64)
	if CheckPreflightBinding(&v, changed) == nil {
		t.Fatal("selector mismatch")
	}
	for _, n := range []int{-1, 1, 2, 255, 256} {
		x := ObservePreflightInvocation(f.Stdout, &n)
		if x.Result != nil || x.Status != "unknown" || *x.ProcessExit != n {
			t.Fatal("process coherence")
		}
	}
	if x := ObservePreflightInvocation(f.Stdout, nil); x.Result != nil || x.ProcessExit != nil || x.Status != "unknown" {
		t.Fatal("missing exit")
	}
	for _, bad := range []string{`{}`, `{"result":null,"status":"unknown"}`, `{"result":null,"processExit":null,"status":"succeeded"}`, `{"result":null,"processExit":"0","status":"unknown"}`} {
		if _, e := DecodePreflightInvocation([]byte(bad)); e == nil {
			t.Fatal("bad invocation")
		}
	}
	goodUnknown := []byte(`{"result":null,"processExit":null,"status":"unknown"}`)
	if _, e := DecodePreflightInvocation(goodUnknown); e != nil {
		t.Fatal(e)
	}
	// Every catalog entry has a valid conservative failure construction.
	if len(preflightMessages) != 17 {
		t.Fatal("catalog drift")
	}
	for code, msg := range preflightMessages {
		t.Run(code, func(t *testing.T) {
			base := preflightFixtureRead(t, "preview-limit-failure.json")
			var m map[string]any
			_ = json.Unmarshal(base.Stdout, &m)
			m["resourcesTruncated"] = code == "PREFLIGHT_LIMIT_EXCEEDED"
			m["errors"] = []any{map[string]any{"code": code, "message": msg}}
			switch code {
			case "INVALID_ARGUMENTS", "UNSUPPORTED_OPERATION", "UNSUPPORTED_CONTRACT", "UNSUPPORTED_FORMAT", "INVALID_TARGET", "UNSUPPORTED_PREFLIGHT":
				m["target"] = nil
				m["outcome"] = "invalid_input"
				m["coordinationEffects"] = "none"
			case "CONFIG_INVALID":
				m["outcome"] = "invalid_input"
				m["coordinationEffects"] = "none"
			case "SECRET_UNAVAILABLE":
				m["target"] = nil
				m["stack"] = ""
				m["coordinationEffects"] = "none"
			case "EVALUATION_FAILED", "QUALIFICATION_FAILED":
				m["target"] = nil
				m["coordinationEffects"] = "none"
			case "COORDINATION_UNAVAILABLE", "ENGINE_CLOSE_FAILED", "COORDINATION_COMMIT_FAILED", "COORDINATION_RELEASE_FAILED":
				m["outcome"] = "unknown"
				m["coordinationEffects"] = "unknown"
			case "CANCELLED":
				m["outcome"] = "cancelled"
			}
			b := preflightJSON(t, m)
			r, e := DecodePreflightResult(b)
			if e != nil {
				t.Fatal("valid failure", e)
			}
			n := 1
			if r.Outcome == "invalid_input" {
				n = 2
			}
			iv := ObservePreflightInvocation(b, &n)
			if iv.Result == nil || iv.Status == "succeeded" {
				t.Fatal("failure axes")
			}
			m["errors"].([]any)[0].(map[string]any)["message"] = "/private/SDK/Pkl/sentinel"
			preflightReject(t, preflightJSON(t, m))
		})
	}
	// Primary + ordered cleanup, four rows maximum; none may retain approval.
	f = preflightFixtureRead(t, "preview-limit-failure.json")
	var m map[string]any
	_ = json.Unmarshal(f.Stdout, &m)
	m["outcome"] = "unknown"
	m["coordinationEffects"] = "unknown"
	errors := m["errors"].([]any)
	for _, code := range []string{"ENGINE_CLOSE_FAILED", "COORDINATION_COMMIT_FAILED", "COORDINATION_RELEASE_FAILED"} {
		errors = append(errors, map[string]any{"code": code, "message": preflightMessages[code]})
	}
	m["errors"] = errors
	if _, e := DecodePreflightResult(preflightJSON(t, m)); e != nil {
		t.Fatal("ordered cleanup", e)
	}
	errors[1], errors[2] = errors[2], errors[1]
	preflightReject(t, preflightJSON(t, m))
}

func TestPreflightCapabilitiesClosed(t *testing.T) {
	for _, name := range []string{"capabilities-supported.json", "capabilities-unsupported.json"} {
		f := preflightFixtureRead(t, name)
		var base map[string]any
		_ = json.Unmarshal(f.Stdout, &base)
		for key := range base {
			var m map[string]any
			_ = json.Unmarshal(f.Stdout, &m)
			delete(m, key)
			if _, e := DecodePreflightCapabilities(preflightJSON(t, m)); e == nil {
				t.Fatal("missing caps key")
			}
			m[key] = nil
			if _, e := DecodePreflightCapabilities(preflightJSON(t, m)); e == nil {
				t.Fatal("null caps key")
			}
		}
		base["nativePlacement"] = true
		if _, e := DecodePreflightCapabilities(preflightJSON(t, base)); e == nil {
			t.Fatal("unknown caps")
		}
	}
}

// Canonical commitment functions are TEST ONLY; consumers must not attempt to
// recompute provider-private input, live facts or store scope from public rows.
func TestPreflightFrozenProvenanceAndCanonicalVectors(t *testing.T) {
	files, e := filepath.Glob("testdata/preflight-v1/*.json")
	if e != nil || len(files) != 15 {
		t.Fatal("corpus")
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		sum := sha256.Sum256(b)
		fmt.Fprintf(h, "%s%c%x%c", filepath.Base(p), 0, sum, 0)
	}
	if hex.EncodeToString(h.Sum(nil)) != "37291da8284dbae80e118f740a356f00488232031a78feb658542594135363d6" {
		t.Fatal("frozen corpus changed")
	}
	b, e := os.ReadFile("testdata/preflight-v1/canonical-vectors.json")
	if e != nil {
		t.Fatal(e)
	}
	var vectors struct {
		Store            struct{ KeyHex, Device, Inode, Euid, MessageHex, PrivateExpectedTokenHex string }
		Input, Selection struct {
			Domain                  string
			Payload                 json.RawMessage
			CanonicalUtf8, Expected string
		}
	}
	if json.Unmarshal(b, &vectors) != nil {
		t.Fatal("vectors")
	}
	for _, v := range []struct {
		Domain                  string
		Payload                 json.RawMessage
		CanonicalUtf8, Expected string
	}{vectors.Input, vectors.Selection} {
		d := json.NewDecoder(bytes.NewReader(v.Payload))
		d.UseNumber()
		var x any
		if d.Decode(&x) != nil {
			t.Fatal("payload")
		}
		canonical := preflightJSON(t, x)
		if string(canonical) != v.CanonicalUtf8 {
			t.Fatal("canonical bytes")
		}
		sum := sha256.Sum256(append([]byte(v.Domain+"\x00"), canonical...))
		if "sha256:"+hex.EncodeToString(sum[:]) != v.Expected {
			t.Fatal("commitment")
		}
	}
	key, e := hex.DecodeString(vectors.Store.KeyHex)
	if e != nil || len(key) != 32 {
		t.Fatal("synthetic key")
	}
	message := []byte("dill.named-store/1\x00")
	for _, s := range []string{vectors.Store.Device, vectors.Store.Inode, vectors.Store.Euid} {
		n, e := strconv.ParseUint(s, 10, 64)
		if e != nil {
			t.Fatal(e)
		}
		message = binary.BigEndian.AppendUint64(message, n)
	}
	if hex.EncodeToString(message) != vectors.Store.MessageHex {
		t.Fatal("store bytes")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	if hex.EncodeToString(mac.Sum(nil)) != vectors.Store.PrivateExpectedTokenHex {
		t.Fatal("store vector")
	}
	// Desired provenance is retained separately from prepared ID/dependencies/flags.
	var input map[string]any
	_ = json.Unmarshal(vectors.Input.Payload, &input)
	service := input["services"].([]any)[0].(map[string]any)
	if service["desiredConfigHash"] == service["preparedImageId"] || service["dependsOn"] == nil || input["flags"].(map[string]any)["callerSource"] != "brine" {
		t.Fatal("synthetic provenance representation")
	}
}
