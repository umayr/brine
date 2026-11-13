package dill

import (
	"strings"
	"time"
	"unicode/utf8"
)

// PreflightArguments is structural input ONLY. None of these strings proves
// native placement, filesystem safety, source provenance or execution authority.
// In particular this does not replace runtime ValidateQualifiedOperationConfig.
type PreflightArguments struct {
	Operation, ConfigFile, Socket, Stack string
	Coordination                         QualifiedOperationConfig
	ForceRecreate                        bool
}

// BuildPreflightArguments emits the bounded full-stack Brine profile. It never
// reads files, probes an engine, launches Dill or enables an execution route.
func BuildPreflightArguments(c PreflightArguments) ([]string, error) {
	return buildPreflightArguments(c, "")
}

// BuildBoundPreflightArguments constructs grammar, NOT an execution permit.
// selectionDigest is already prefixed and must be passed verbatim.
func BuildBoundPreflightArguments(c PreflightArguments, selectionDigest string) ([]string, error) {
	if !operationDigestPattern.MatchString(selectionDigest) {
		return nil, ErrPreflightArguments
	}
	return buildPreflightArguments(c, selectionDigest)
}
func buildPreflightArguments(c PreflightArguments, digest string) ([]string, error) {
	verb, contract := "preflight", PreflightVersion
	if digest != "" {
		verb, contract = "operation", OperationVersion
	}
	a := []string{verb, c.Operation, "--contract", contract, "--format", "json", "--engine", "docker", "--engine-socket", c.Socket, "-p", c.Stack, "-f", c.ConfigFile, "--timeout", "5m", "--coordination-layout", c.Coordination.Layout, "--coordination-daemon-id", c.Coordination.DaemonID, "--coordination-state-dir", c.Coordination.StateDir, "--coordination-source", "brine"}
	if c.Operation == "up" {
		a = append(a, "--no-prune", "--pull", "never")
		if c.ForceRecreate {
			a = append(a, "--force-recreate")
		}
	} else if c.ForceRecreate {
		return nil, ErrPreflightArguments
	}
	if digest != "" {
		a = append(a, "--preflight-contract", PreflightVersion, "--expect-preflight", digest)
	}
	if ValidatePreflightArguments(a) != nil {
		return nil, ErrPreflightArguments
	}
	return a, nil
}

// ValidatePreflightArguments checks only the closed Brine structural profile.
// Go flag single/double dash, equals/separate string values and stack aliases
// are accepted; booleans use Go flag bare/equals syntax. Duplicate semantic
// fields (including aliases), operands and even explicitly false denied flags
// reject. Values are not scanned for flag-like substrings. No OS is inferred.
func ValidatePreflightArguments(a []string) error {
	if len(a) < 2 || len(a) > 64 || !oneOf(a[0], "preflight", "operation") || !preflightAction(a[1]) {
		return ErrPreflightArguments
	}
	m := map[string]string{}
	total := 0
	for _, v := range a {
		total += len(v)
		if !utf8.ValidString(v) || strings.ContainsAny(v, "\x00\r\n") {
			return ErrPreflightArguments
		}
	}
	if total > PreflightMaxOutputBytes {
		return ErrPreflightArguments
	}
	for i := 2; i < len(a); i++ {
		t := a[i]
		if len(t) < 2 || t[0] != '-' || t == "--" {
			return ErrPreflightArguments
		}
		t = t[1:]
		if strings.HasPrefix(t, "-") {
			t = t[1:]
		}
		key, value, eq := strings.Cut(t, "=")
		// "stack" is only our semantic map key, never a frozen raw alias.
		if key == "stack" {
			return ErrPreflightArguments
		}
		switch key {
		case "p", "stack-name", "project-name":
			key = "stack"
		}
		boolean := oneOf(key, "quiet", "force-recreate", "no-prune")
		if !boolean && !oneOf(key, "f", "stack", "engine", "engine-socket", "timeout", "contract", "format", "coordination-layout", "coordination-daemon-id", "coordination-state-dir", "coordination-source", "preflight-contract", "expect-preflight", "pull") {
			return ErrPreflightArguments
		}
		if _, ok := m[key]; ok {
			return ErrPreflightArguments
		}
		if boolean {
			if !eq {
				value = "true"
			}
			if !oneOf(value, "true", "false") {
				return ErrPreflightArguments
			}
		} else if !eq {
			i++
			if i >= len(a) {
				return ErrPreflightArguments
			}
			value = a[i]
		}
		if value == "" {
			return ErrPreflightArguments
		}
		m[key] = value
	}
	for _, k := range []string{"f", "stack", "engine", "engine-socket", "timeout", "contract", "format", "coordination-layout", "coordination-daemon-id", "coordination-state-dir", "coordination-source"} {
		if m[k] == "" {
			return ErrPreflightArguments
		}
	}
	if m["engine"] != "docker" || !operationStackPattern.MatchString(m["stack"]) || m["format"] != "json" || m["coordination-layout"] != "native-local-docker-v1" || m["coordination-source"] != "brine" {
		return ErrPreflightArguments
	}
	duration, e := time.ParseDuration(m["timeout"])
	if e != nil || duration <= 0 {
		return ErrPreflightArguments
	}
	if a[1] == "up" {
		if m["no-prune"] != "true" || m["pull"] != "never" {
			return ErrPreflightArguments
		}
	} else {
		for _, k := range []string{"no-prune", "pull", "force-recreate"} {
			if _, ok := m[k]; ok {
				return ErrPreflightArguments
			}
		}
	}
	if a[0] == "preflight" {
		if m["contract"] != PreflightVersion || m["preflight-contract"] != "" || m["expect-preflight"] != "" {
			return ErrPreflightArguments
		}
	} else if m["contract"] != OperationVersion || m["preflight-contract"] != PreflightVersion || !operationDigestPattern.MatchString(m["expect-preflight"]) {
		return ErrPreflightArguments
	}
	return nil
}
