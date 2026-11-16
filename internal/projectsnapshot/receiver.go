package projectsnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// ValidateProvisionalReceiver checks the versioned provisional Dill staging
// service and receiver contract. This is duplicated client-side guidance, not authorization:
// the receiving server must revalidate the complete snapshot before accepting it.
// Errors intentionally contain neither paths nor file contents.
func ValidateProvisionalReceiver(ctx context.Context, s Snapshot) error {
	invalid := errors.New("project is incompatible with the provisional snapshot receiver")
	if ctx == nil {
		return invalid
	}
	if len(s.Files) > 1000 {
		return invalid
	}
	files := map[string]bool{}
	dirs := map[string]string{}
	var total int64
	found := false
	for _, e := range s.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := e.Path
		if len(p) == 0 || len(p) > 512 {
			return invalid
		}
		parts := strings.Split(p, "/")
		if len(parts) > 32 {
			return invalid
		}
		for _, c := range parts {
			if len(c) == 0 || len(c) > 255 || c == "." || c == ".." || strings.EqualFold(c, ".git") || c[len(c)-1] == ' ' || c[len(c)-1] == '.' {
				return invalid
			}
			for _, r := range c {
				if r < 32 || r > 126 || strings.ContainsRune(`\:<>"|?*`, r) {
					return invalid
				}
			}
			base := strings.ToUpper(strings.SplitN(c, ".", 2)[0])
			if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
				return invalid
			}
		}
		n := int64(len(e.Content))
		if n > 4<<20 || n > 16<<20-total {
			return invalid
		}
		total += n
		key := strings.ToLower(p)
		if files[key] || dirs[key] != "" {
			return invalid
		}
		for i := 1; i < len(parts); i++ {
			dir := strings.Join(parts[:i], "/")
			k := strings.ToLower(dir)
			if files[k] {
				return invalid
			}
			if prev := dirs[k]; prev != "" && prev != dir {
				return invalid
			}
			if dirs[k] == "" && len(dirs)+1 >= 255 {
				return invalid
			}
			dirs[k] = dir
		}
		files[key] = true
		if sha256.Sum256(e.Content) != e.SHA256 {
			return invalid
		}
		if p == s.Entrypoint {
			found = true
		}
	}
	if !found || provisionalWireSize(s) > 24<<20 {
		return invalid
	}
	return ctx.Err()
}

// provisionalWireSize counts a compact encoding/json upload with canonical padded
// base64 and lowercase hex, without allocating or serializing sensitive contents.
// Any future uploader must use this exact encoding or validate its actual body.
func provisionalWireSize(s Snapshot) int64 {
	entry, _ := json.Marshal(s.Entrypoint)
	n := int64(len(`{"schema":"dill.snapshot.v1","entrypoint":`) + len(entry) + len(`,"files":[`) + len(`]}`))
	for i, f := range s.Files {
		if i > 0 {
			n++
		}
		// Marshal the small metadata object with an empty bytes field; only the
		// base64 expansion remains to be counted.
		meta, _ := json.Marshal(struct {
			Path   string `json:"path"`
			Bytes  string `json:"bytes"`
			Length int    `json:"length"`
			SHA256 string `json:"sha256"`
		}{f.Path, "", len(f.Content), hex.EncodeToString(f.SHA256[:])})
		n += int64(len(meta)) + int64(base64.StdEncoding.EncodedLen(len(f.Content)))
	}
	return n
}
