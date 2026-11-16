package dillserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Manifest is a validated catalog manifest. Its fields are private so callers
// cannot mutate the validated result. Accessors return values or fresh copies.
// It is metadata, potentially sensitive, not proof of target or engine identity,
// sandboxing, future stage contents, or consent to deploy.
type Manifest struct {
	raw, entrypoint string
	digest          [32]byte
	files           []ManifestFile
}
type ManifestFile struct {
	Path   string
	Size   int64
	SHA256 [32]byte
}

func (m Manifest) Raw() string           { return m.raw }
func (m Manifest) Entrypoint() string    { return m.entrypoint }
func (m Manifest) Digest() [32]byte      { return m.digest }
func (m Manifest) Files() []ManifestFile { return append([]ManifestFile(nil), m.files...) }

type manifestWireFile struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
	SHA256 string `json:"sha256"`
}
type manifestWire struct {
	Schema     string             `json:"schema"`
	Entrypoint string             `json:"entrypoint"`
	Files      []manifestWireFile `json:"files"`
}

// validateManifest uses byte equality with Go's canonical compact encoding to
// reject duplicate, missing, unknown, reordered and noncanonical fields.
func validateManifest(ctx context.Context, raw []byte, digest [32]byte) (Manifest, error) {
	var out Manifest
	if ctx == nil || ctx.Err() != nil || !utf8.Valid(raw) || sha256.Sum256(raw) != digest {
		return out, ErrUnavailable
	}
	var w manifestWire
	if json.Unmarshal(raw, &w) != nil || w.Schema != "dill.snapshot.manifest.v1" || len(w.Files) == 0 || len(w.Files) > 1000 {
		return out, ErrUnavailable
	}
	canonical, err := json.Marshal(w)
	if err != nil || string(canonical) != string(raw) {
		return out, ErrUnavailable
	}
	files := make(map[string]bool)
	dirs := make(map[string]string)
	var total int64
	found := false
	prev := ""
	result := make([]ManifestFile, 0, len(w.Files))
	for _, f := range w.Files {
		if ctx.Err() != nil || len(f.Path) == 0 || len(f.Path) > 512 || (prev != "" && f.Path <= prev) || f.Length < 0 || f.Length > 4<<20 || f.Length > (16<<20)-total {
			return out, ErrUnavailable
		}
		prev = f.Path
		total += f.Length
		parts := strings.Split(f.Path, "/")
		if len(parts) > 32 {
			return out, ErrUnavailable
		}
		for _, c := range parts {
			if len(c) == 0 || len(c) > 255 || c == "." || c == ".." || strings.EqualFold(c, ".git") || c[len(c)-1] == ' ' || c[len(c)-1] == '.' {
				return out, ErrUnavailable
			}
			for _, r := range c {
				if r < 32 || r > 126 || strings.ContainsRune(`\:<>"|?*`, r) {
					return out, ErrUnavailable
				}
			}
			base := strings.ToUpper(strings.SplitN(c, ".", 2)[0])
			if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
				return out, ErrUnavailable
			}
		}
		key := strings.ToLower(f.Path)
		if files[key] || dirs[key] != "" {
			return out, ErrUnavailable
		}
		for i := 1; i < len(parts); i++ {
			dir := strings.Join(parts[:i], "/")
			k := strings.ToLower(dir)
			if files[k] || (dirs[k] != "" && dirs[k] != dir) || (dirs[k] == "" && len(dirs)+1 >= 255) {
				return out, ErrUnavailable
			}
			dirs[k] = dir
		}
		files[key] = true
		b, e := hex.DecodeString(f.SHA256)
		if e != nil || len(b) != 32 || hex.EncodeToString(b) != f.SHA256 {
			return out, ErrUnavailable
		}
		var hash [32]byte
		copy(hash[:], b)
		result = append(result, ManifestFile{f.Path, f.Length, hash})
		if f.Path == w.Entrypoint {
			found = true
		}
	}
	if !found || ctx.Err() != nil {
		return out, ErrUnavailable
	}
	return Manifest{raw: string(raw), entrypoint: w.Entrypoint, digest: digest, files: result}, nil
}
