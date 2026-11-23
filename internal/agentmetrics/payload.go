package agentmetrics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

// sample is an observation only; decoding does not authenticate its source.
type sample struct {
	HostTotal, HostAvailable, OwnedCount, OwnedUsage uint64
	HasHost, HasOwnedCount, HasOwnedUsage            bool
}

// decodeSample accepts only the success body of an already proof-verified request.
func decodeSample(body []byte, requestID string) (sample, error) {
	var out sample
	bad := func() (sample, error) { return sample{}, errors.New("invalid sample envelope") }
	if len(body) > 4096 || !utf8.Valid(body) || requestID == "" {
		return bad()
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	v, err := readValue(d, 1)
	if err != nil {
		return bad()
	}
	if _, err = d.Token(); err != io.EOF {
		return bad()
	}
	root, ok := v.(map[string]any)
	if !ok || len(root) != 3 || root["version"] != "brine.agent.read/1" || root["requestId"] != requestID {
		return bad()
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return bad()
	}
	allowed := map[string]bool{"capability": true, "coverage": true, "hostMemoryComplete": true, "ownedMemoryComplete": true, "hostMemoryTotalBytes": true, "hostMemoryAvailableBytes": true, "ownedRunningContainers": true, "ownedMemoryUsageBytes": true}
	for k := range data {
		if !allowed[k] {
			return bad()
		}
	}
	if data["capability"] != "brine.agent.metrics.aggregate/1" {
		return bad()
	}
	hc, ok := data["hostMemoryComplete"].(bool)
	if !ok {
		return bad()
	}
	oc, ok := data["ownedMemoryComplete"].(bool)
	if !ok {
		return bad()
	}
	number := func(key string) (uint64, bool, error) {
		v, exists := data[key]
		if !exists {
			return 0, false, nil
		}
		n, ok := v.(json.Number)
		if !ok {
			return 0, false, errors.New("not integer")
		}
		s := string(n)
		if len(s) == 0 || (len(s) > 1 && s[0] == '0') {
			return 0, false, errors.New("integer format")
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, false, errors.New("integer format")
			}
		}
		x, e := strconv.ParseUint(s, 10, 64)
		if e != nil || x > 9007199254740991 {
			return 0, false, errors.New("integer range")
		}
		return x, true, nil
	}
	var ht, ha bool
	if out.HostTotal, ht, err = number("hostMemoryTotalBytes"); err != nil {
		return bad()
	}
	if out.HostAvailable, ha, err = number("hostMemoryAvailableBytes"); err != nil {
		return bad()
	}
	if out.OwnedCount, out.HasOwnedCount, err = number("ownedRunningContainers"); err != nil {
		return bad()
	}
	if out.OwnedUsage, out.HasOwnedUsage, err = number("ownedMemoryUsageBytes"); err != nil {
		return bad()
	}
	if ht != ha || hc != ht || oc != out.HasOwnedUsage {
		return bad()
	}
	out.HasHost = ht
	if ht && (out.HostTotal == 0 || out.HostAvailable > out.HostTotal || out.HostTotal%1024 != 0 || out.HostAvailable%1024 != 0) {
		return bad()
	}
	if out.HasOwnedUsage && (!out.HasOwnedCount || out.OwnedCount == 0) {
		return bad()
	}
	if !ht && !out.HasOwnedUsage {
		return bad()
	}
	coverage := "partial"
	if ht && out.HasOwnedUsage {
		coverage = "complete"
	}
	if data["coverage"] != coverage {
		return bad()
	}
	return out, nil
}

// readValue enforces depth and duplicate keys before schema interpretation.
func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > 4 {
		return nil, errors.New("depth")
	}
	t, e := d.Token()
	if e != nil {
		return nil, e
	}
	if t == nil {
		return nil, errors.New("null")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, e
			}
			s, ok := k.(string)
			if !ok {
				return nil, errors.New("key")
			}
			if _, exists := m[s]; exists {
				return nil, errors.New("duplicate")
			}
			v, e := readValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			m[s] = v
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, errors.New("object")
		}
		return m, nil
	case '[':
		var a []any
		for d.More() {
			v, e := readValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, errors.New("array")
		}
		return a, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter")
	}
}
