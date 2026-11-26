package webhooks

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errDestination = errors.New("webhook destination is not permitted")

func ValidateDestination(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.HasSuffix(u.Host, ":") {
		return errDestination
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.ContainsAny(host, "% \\/") {
		return errDestination
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return errDestination
		}
	}
	if ip, e := netip.ParseAddr(host); e == nil && forbiddenIP(ip) {
		return errDestination
	}
	return nil
}

// These addresses cannot be enabled through the private-receiver allowlist.
func forbiddenIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// Shared space, benchmarking, transition mechanisms, documentation/reserved
	// ranges and metadata/credential endpoints are not integration destinations.
	for _, p := range deniedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var deniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	// AWS task credentials and Alibaba metadata live in otherwise private/shared ranges.
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("168.63.129.16/32"), // Azure platform virtual address.
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

type deliveryNetwork struct {
	allowed []netip.Prefix
	lookup  func(context.Context, string) ([]netip.Addr, error)
	dial    func(context.Context, string, string) (net.Conn, error)
}

func (n *deliveryNetwork) permits(ip netip.Addr) bool {
	ip = ip.Unmap()
	if forbiddenIP(ip) {
		return false
	}
	if !ip.IsPrivate() {
		return true
	}
	for _, p := range n.allowed {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func (n *deliveryNetwork) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errDestination
	}
	ips, err := n.lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errDestination
	}
	for _, ip := range ips {
		if !n.permits(ip) {
			return nil, errDestination
		}
	}
	// No second DNS lookup: connect only to the validated numeric address. The
	// HTTP transport retains the original hostname for certificate verification.
	for _, ip := range ips {
		c, e := n.dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if e == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("webhook connection unavailable")
}

func NewDeliveryClient(allowedCIDRs []string) (*http.Client, error) {
	n := &deliveryNetwork{}
	for _, raw := range allowedCIDRs {
		p, e := netip.ParsePrefix(raw)
		if e != nil || p.Addr().Is4In6() || !p.Addr().IsPrivate() || forbiddenIP(p.Addr()) {
			return nil, errDestination
		}
		p = p.Masked()
		// Require a subnet entirely within a private address block; /0 and broad
		// prefixes that happen to begin with a private address are not accepted.
		private := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("fc00::/7")}
		valid := false
		for _, block := range private {
			if block.Contains(p.Addr()) && p.Bits() >= block.Bits() {
				valid = true
			}
		}
		if !valid {
			return nil, errDestination
		}
		n.allowed = append(n.allowed, p)
	}
	n.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	n.dial = d.DialContext
	transport := &http.Transport{Proxy: nil, DialContext: n.dialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, DisableCompression: true, MaxResponseHeaderBytes: 32 << 10}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
