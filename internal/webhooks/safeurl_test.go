package webhooks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"testing"
)

func TestValidateDestination(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://user:password@example.com", "https://example.com?token=private", "https://example.com?", "https://example.com/#fragment", "https://localhost/a", "https://sub.localhost/a", "https://127.0.0.1/a", "https://[::1]/a", "https://169.254.169.254/", "https://example.com:0/", "https://example.com:65536/", "https://example.com:/", "https:///missing"} {
		if ValidateDestination(raw) == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"https://hooks.example.com/events", "https://hooks.example.com:8443/events", "https://10.0.0.1/events"} {
		if e := ValidateDestination(raw); e != nil {
			t.Errorf("rejected configuration %q: %v", raw, e)
		}
	}
}
func TestDestinationIPPolicy(t *testing.T) {
	n := deliveryNetwork{allowed: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fc00::/7")}}
	for _, ip := range []string{"0.0.0.0", "127.0.0.1", "::1", "169.254.169.254", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "224.0.0.1", "ff02::1", "100.100.100.200", "fd00:ec2::254", "2002:7f00:1::", "64:ff9b::7f00:1", "192.168.1.1"} {
		if n.permits(netip.MustParseAddr(ip)) {
			t.Errorf("accepted %s", ip)
		}
	}
	for _, ip := range []string{"10.1.2.3", "::ffff:10.1.2.3", "2606:4700::1111", "1.1.1.1"} {
		if !n.permits(netip.MustParseAddr(ip)) {
			t.Errorf("rejected %s", ip)
		}
	}
}
func TestPinnedDNSRejectsMixedAnswers(t *testing.T) {
	called := false
	n := deliveryNetwork{lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("127.0.0.1")}, nil
	}, dial: func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("offline")
	}}
	if _, e := n.dialContext(context.Background(), "tcp", "hooks.example.com:443"); e == nil || called {
		t.Fatal("mixed DNS allowed a dial")
	}
	n.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	n.dial = func(_ context.Context, _, address string) (net.Conn, error) {
		called = true
		if address != "1.1.1.1:443" {
			t.Fatalf("not pinned: %s", address)
		}
		return nil, errors.New("offline")
	}
	n.dialContext(context.Background(), "tcp", "hooks.example.com:443")
	if !called {
		t.Fatal("validated IP not dialed")
	}
}
func TestDeliveryClientPolicy(t *testing.T) {
	for _, cidr := range []string{"0.0.0.0/0", "10.0.0.1/0", "127.0.0.0/8", "169.254.0.0/16", "::/0", "invalid", "fd00:ec2::254/128"} {
		if _, e := NewDeliveryClient([]string{cidr}); e == nil {
			t.Errorf("accepted allowlist %s", cidr)
		}
	}
	c, e := NewDeliveryClient([]string{"10.0.0.0/8"})
	if e != nil {
		t.Fatal(e)
	}
	tr := c.Transport.(*http.Transport)
	if tr.Proxy != nil || tr.TLSClientConfig.InsecureSkipVerify || c.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("unsafe transport")
	}
}
