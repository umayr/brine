package pkl

import (
	"regexp"
	"strings"
)

var enginePattern = regexp.MustCompile(`(?m)\bengine\s*=\s*"(docker|podman)"`)

// DeclaredEngine returns the first Docker-compatible engine declared in Pkl
// source, ignoring commented-out declarations.
func DeclaredEngine(src string) string {
	match := enginePattern.FindStringSubmatch(stripComments(src))
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func stripComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))

	inLineComment := false
	inBlockComment := false
	inString := false
	escaped := false

	for i := 0; i < len(src); i++ {
		ch := src[i]
		next := byte(0)
		if i+1 < len(src) {
			next = src[i+1]
		}

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				out.WriteByte(ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if inString {
			out.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '/' && next == '/' {
			inLineComment = true
			i++
			continue
		}
		if ch == '/' && next == '*' {
			inBlockComment = true
			i++
			continue
		}
		if ch == '"' {
			inString = true
		}
		out.WriteByte(ch)
	}

	return out.String()
}
