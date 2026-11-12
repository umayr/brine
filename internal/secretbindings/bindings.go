// Package secretbindings holds reference-only Alpha secret deployment state.
// It does not resolve sources or evaluate Pkl.
package secretbindings

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid secret binding document")
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

const Version = "dill.secret-bindings/1"
const MaxDocument = 64 * 1024

type Binding struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Revision string `json:"revision"`
}
type Document struct {
	Version  string    `json:"version"`
	Target   string    `json:"target"`
	Stack    string    `json:"stack"`
	Bindings []Binding `json:"bindings"`
}

func object(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, ErrInvalid
	}
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		tok, err = d.Token()
		k, ok := tok.(string)
		if err != nil || !ok || !allowed[k] {
			return nil, ErrInvalid
		}
		if _, ok = result[k]; ok {
			return nil, ErrInvalid
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, ErrInvalid
		}
		result[k] = raw
	}
	tok, err = d.Token()
	if err != nil || tok != json.Delim('}') {
		return nil, ErrInvalid
	}
	if _, err = d.Token(); err != io.EOF || len(result) != len(keys) {
		return nil, ErrInvalid
	}
	return result, nil
}
func text(raw json.RawMessage) (string, error) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil || len(s) > 128 {
		return "", ErrInvalid
	}
	return s, nil
}

// Parse rejects ambiguous JSON, values outside the reference grammar, and
// documents whose scope differs from the trusted deployment selection.
func Parse(data []byte, stack, target string) (Document, error) {
	var out Document
	if len(data) > MaxDocument || !utf8.Valid(data) || !namePattern.MatchString(stack) || !namePattern.MatchString(target) {
		return out, ErrInvalid
	}
	m, err := object(data, "version", "target", "stack", "bindings")
	if err != nil {
		return out, err
	}
	if out.Version, err = text(m["version"]); err != nil || out.Version != Version {
		return Document{}, ErrInvalid
	}
	if out.Stack, err = text(m["stack"]); err != nil || out.Stack != stack {
		return Document{}, ErrInvalid
	}
	if out.Target, err = text(m["target"]); err != nil || out.Target != target {
		return Document{}, ErrInvalid
	}
	raw := bytes.TrimSpace(m["bindings"])
	if len(raw) == 0 || raw[0] != '[' {
		return Document{}, ErrInvalid
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || len(entries) > 256 {
		return Document{}, ErrInvalid
	}
	out.Bindings = make([]Binding, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		v, e := object(entry, "name", "source", "revision")
		if e != nil {
			return Document{}, ErrInvalid
		}
		var b Binding
		if b.Name, e = text(v["name"]); e != nil || !namePattern.MatchString(b.Name) || seen[b.Name] {
			return Document{}, ErrInvalid
		}
		seen[b.Name] = true
		if b.Source, e = text(v["source"]); e != nil || !namePattern.MatchString(b.Source) {
			return Document{}, ErrInvalid
		}
		if b.Revision, e = text(v["revision"]); e != nil || !namePattern.MatchString(b.Revision) {
			return Document{}, ErrInvalid
		}
		out.Bindings = append(out.Bindings, b)
	}
	return out, nil
}
