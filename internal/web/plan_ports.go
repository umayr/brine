package web

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// describePlanPort formats the v0.5 Dill field representation without
// guessing about unknown formats or equating IPv4 and IPv6 bindings.
func describePlanPort(field, value string) string {
	fallback := func() string { return "Unrecognized port value: " + value }
	if !strings.HasPrefix(field, "port.") {
		return fallback()
	}
	key := strings.TrimPrefix(field, "port.")
	target, protocol, ok := strings.Cut(key, "/")
	if !ok || !validPlanPort(target) || (protocol != "tcp" && protocol != "udp" && protocol != "sctp") {
		return fallback()
	}
	proto := strings.ToUpper(protocol)
	if value == target {
		return fmt.Sprintf("Container port %s / %s; no host binding reported", target, proto)
	}
	host, dest, ok := strings.Cut(value, "->")
	if !ok || dest != target || strings.Contains(dest, "->") {
		return fallback()
	}
	address := "Default host interface"
	published := host
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		ip := host[:i]
		published = host[i+1:]
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return fallback()
		}
		switch ip {
		case "0.0.0.0":
			address = "All IPv4 interfaces (0.0.0.0)"
		case "::":
			address = "All IPv6 interfaces (::)"
		default:
			address = "Host address " + ip
		}
	}
	if !validPlanPort(published) {
		return fallback()
	}
	return fmt.Sprintf("%s · host port %s → container port %s / %s", address, published, target, proto)
}

func validPlanPort(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(value)
	return err == nil && n > 0 && n <= 65535
}
