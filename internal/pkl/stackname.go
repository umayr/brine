package pkl

import (
	"regexp"
	"strconv"
)

var configOpenRe = regexp.MustCompile(`(?m)^[ \t]*config[ \t]*\{`)

// WithStackName ensures the top-level config.name matches the stack name.
//
// It rewrites the value only when it is empty or the starter placeholder
// "my-app"; an explicitly customised name is left alone. When no name is
// present it is inserted into the config block. The returned string is
// unchanged if no config block can be found.
func WithStackName(src, name string) string {
	if name == "" {
		return src
	}
	open := findConfigOpen(src)
	if open < 0 {
		return src
	}
	closeIdx := matchBrace(src, open)
	if closeIdx < 0 {
		return src
	}
	body := src[open+1 : closeIdx]

	start, end, value, ok := findTopLevelName(body)
	if ok {
		if value != "" && value != "my-app" {
			return src // respect an explicit name
		}
		return src[:open+1+start] + strconv.Quote(name) + src[open+1+end:]
	}

	// No name inside the config block: insert one directly after the brace.
	insert := "\n  name = " + strconv.Quote(name) + "\n"
	return src[:open+1] + insert + src[open+1:]
}

// DeclaredName returns the top-level config.name value, or "" if unset.
func DeclaredName(src string) string {
	open := findConfigOpen(src)
	if open < 0 {
		return ""
	}
	closeIdx := matchBrace(src, open)
	if closeIdx < 0 {
		return ""
	}
	_, _, value, _ := findTopLevelName(src[open+1 : closeIdx])
	return value
}

// findTopLevelName locates `name = "..."` at depth 0 within a config body and
// returns the span of the quoted value (including quotes) plus its contents.
func findTopLevelName(body string) (start, end int, value string, ok bool) {
	depth := 0
	for i := 0; i < len(body); {
		switch {
		case body[i] == '/' && i+1 < len(body) && body[i+1] == '/':
			i = skipLineComment(body, i)
			continue
		case body[i] == '/' && i+1 < len(body) && body[i+1] == '*':
			i = skipBlockComment(body, i)
			continue
		case body[i] == '"':
			i = skipString(body, i)
			continue
		}

		ch := body[i]
		if ch == '{' {
			depth++
			i++
			continue
		}
		if ch == '}' {
			if depth > 0 {
				depth--
			}
			i++
			continue
		}

		if depth == 0 && ch == 'n' && hasIdentStart(body, i) && hasPrefixWord(body, i, "name") {
			j := skipSpace(body, i+4)
			if j < len(body) && body[j] == '=' {
				j = skipSpace(body, j+1)
				if j < len(body) && body[j] == '"' {
					valueStart := j
					valueEnd := skipString(body, j)
					if valueEnd > valueStart+1 {
						return valueStart, valueEnd, body[valueStart+1 : valueEnd-1], true
					}
				}
			}
		}
		i++
	}
	return 0, 0, "", false
}

func hasIdentStart(s string, i int) bool {
	if i == 0 {
		return true
	}
	c := s[i-1]
	return !(c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
}

func hasPrefixWord(s string, i int, word string) bool {
	if i+len(word) > len(s) || s[i:i+len(word)] != word {
		return false
	}
	if i+len(word) == len(s) {
		return true
	}
	c := s[i+len(word)]
	return !(c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
		i++
	}
	return i
}

// skipString returns the index just past the closing quote at or after i.
func skipString(s string, i int) int {
	// s[i] is the opening quote.
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(s)
}

func skipLineComment(s string, i int) int {
	for j := i + 2; j < len(s); j++ {
		if s[j] == '\n' {
			return j
		}
	}
	return len(s)
}

func skipBlockComment(s string, i int) int {
	for j := i + 2; j+1 < len(s); j++ {
		if s[j] == '*' && s[j+1] == '/' {
			return j + 2
		}
	}
	return len(s)
}

// findConfigOpen returns the index of the '{' that opens the top-level config
// block, or -1.
func findConfigOpen(src string) int {
	loc := configOpenRe.FindStringIndex(src)
	if loc == nil {
		return -1
	}
	return loc[1] - 1
}

// matchBrace returns the index of the '}' matching the '{' at open, skipping
// strings and comments. Returns -1 if the block is unterminated.
func matchBrace(src string, open int) int {
	depth := 0
	for i := open; i < len(src); {
		switch {
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			i = skipLineComment(src, i)
			continue
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skipBlockComment(src, i)
			continue
		case src[i] == '"':
			i = skipString(src, i)
			continue
		}
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}
