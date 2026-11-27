package secrets

import (
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
)

func exactlyOne(options ...bool) bool {
	seen := false
	for _, set := range options {
		if set {
			if seen {
				return false
			}
			seen = true
		}
	}
	return seen
}

// Use a narrow, unescaped ASCII alphabet. A segment cannot start with a dot;
// percent escapes, backslashes, control chars and separators are excluded.
func validName(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '-' || (i != 0 && c == '.')) {
			return false
		}
	}
	return true
}

func validRelativePath(s string) bool {
	if len(s) > 512 {
		return false
	}
	for _, segment := range strings.Split(s, "/") {
		if !validName(segment) {
			return false
		}
	}
	return true
}

func validFolder(s string) bool {
	return s == "/" || strings.HasPrefix(s, "/") && validRelativePath(s[1:])
}

func validFileRef(ref FileRef) bool {
	p := ref.Path
	if len(p) < 2 || len(p) > 4095 || !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < ' ' || p[i] == 0x7f || p[i] == '\\' {
			return false
		}
	}
	for _, segment := range strings.Split(p[1:], "/") {
		if len(segment) > 255 {
			return false
		}
	}
	return true
}

func validSelector(s Selector) bool {
	if s.Version != SchemaVersion || !validName(s.Connection) || !exactlyOne(s.Vault != nil, s.Infisical != nil, s.File != nil) {
		return false
	}
	switch {
	case s.Vault != nil:
		return validRelativePath(s.Vault.Path) && validName(s.Vault.Key)
	case s.Infisical != nil:
		return (s.Infisical.Folder == "" || validFolder(s.Infisical.Folder)) && validName(s.Infisical.Key)
	default:
		return validName(s.File.Source)
	}
}

func parseOrigin(raw string, allowTestHTTP bool) (url.URL, bool) {
	if len(raw) > 512 || strings.ContainsAny(raw, "?#%\\") {
		return url.URL{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" && !(allowTestHTTP && u.Scheme == "http") ||
		u.Opaque != "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return url.URL{}, false
	}
	host := u.Hostname()
	if !validHost(host) {
		return url.URL{}, false
	}
	port := u.Port()
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return url.URL{}, false
		}
		authority = net.JoinHostPort(host, port)
	}
	if authority != u.Host {
		return url.URL{}, false
	}
	return url.URL{Scheme: u.Scheme, Host: authority}, true
}

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
