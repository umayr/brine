package dillserve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/umayr/brine/internal/projectsnapshot"
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

const manifestSchema = "dill.snapshot.manifest.v1"

// SnapshotManifestDigest derives the existing catalog manifest commitment from
// the reviewed bytes, independently of a receiver response. It is not proof of
// staging, consent, or execution. The caller must not mutate s concurrently.
func SnapshotManifestDigest(ctx context.Context, s projectsnapshot.Snapshot) ([sha256.Size]byte, error) {
	if err := projectsnapshot.ValidateProvisionalReceiver(ctx, s); err != nil {
		return [sha256.Size]byte{}, err
	}
	w := manifestWire{Schema: manifestSchema, Entrypoint: s.Entrypoint, Files: make([]manifestWireFile, 0, len(s.Files))}
	for _, f := range s.Files {
		w.Files = append(w.Files, manifestWireFile{Path: f.Path, Length: int64(len(f.Content)), SHA256: hex.EncodeToString(f.SHA256[:])})
	}
	sort.Slice(w.Files, func(i, j int) bool { return w.Files[i].Path < w.Files[j].Path })
	raw, err := json.Marshal(w)
	if err != nil {
		return [sha256.Size]byte{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

// validateManifest uses byte equality with Go's canonical compact encoding to
// reject duplicate, missing, unknown, reordered and noncanonical fields.
func validateManifest(ctx context.Context, raw []byte, digest [32]byte) (Manifest, error) {
	var out Manifest
	if ctx == nil || ctx.Err() != nil || !utf8.Valid(raw) || sha256.Sum256(raw) != digest {
		return out, ErrUnavailable
	}
	var w manifestWire
	if json.Unmarshal(raw, &w) != nil || w.Schema != manifestSchema || len(w.Files) == 0 || len(w.Files) > 1000 {
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
