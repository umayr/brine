package remoteui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/umayr/brine/internal/auth"
	"github.com/umayr/brine/internal/targetregistry"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var errDeployCredential = errors.New("separate remote deployment credential unavailable")

type deploymentEnrollmentKey struct{}
type DeploymentCredentials interface {
	Credential(context.Context, targetregistry.Record) (string, error)
}
type deploymentCredentialEntry struct {
	Binding    string `json:"binding"`
	Enrollment string `json:"enrollment"`
	TokenFile  string `json:"token_file"`
}
type fileDeploymentCredentials struct {
	dataDir string
	path    string
	digest  [32]byte
	entries map[string]deploymentCredentialEntry
}

func openCredentialParent(path string) (int, error) {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, e := unix.Open("/", flags, 0)
	if e != nil {
		return -1, errDeployCredential
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			next, err := unix.Openat(fd, parts[i], flags, 0)
			unix.Close(fd)
			if err != nil {
				return -1, errDeployCredential
			}
			fd = next
		}
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || !auth.CredentialACLSafe(fd) || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || ((st.Mode&0022) != 0 && !(st.Uid == 0 && st.Mode&01000 != 0)) {
			unix.Close(fd)
			return -1, errDeployCredential
		}
	}
	return fd, nil
}

func privateDeploymentFile(path, dataDir string, max int) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errDeployCredential
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil {
		return nil, errDeployCredential
	}
	root, e := filepath.Abs(dataDir)
	if e != nil {
		return nil, errDeployCredential
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return nil, errDeployCredential
	}
	rel, e := filepath.Rel(root, real)
	if e != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, errDeployCredential
	}
	parent, e := openCredentialParent(filepath.Dir(real))
	if e != nil {
		return nil, errDeployCredential
	}
	defer unix.Close(parent)
	var dir unix.Stat_t
	if unix.Fstat(parent, &dir) != nil || dir.Uid != uint32(os.Geteuid()) || dir.Mode&07777 != 0700 || !auth.CredentialACLSafe(parent) {
		return nil, errDeployCredential
	}
	fd, e := unix.Openat(parent, filepath.Base(real), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, errDeployCredential
	}
	f := os.NewFile(uintptr(fd), "remote-deploy-credential")
	defer f.Close()
	var before, after, entry unix.Stat_t
	safe := func(st unix.Stat_t) bool {
		return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&07777 == 0600 && st.Uid == uint32(os.Geteuid()) && st.Nlink == 1 && st.Size <= int64(max)
	}
	if unix.Fstat(fd, &before) != nil || !safe(before) || !auth.CredentialACLSafe(fd) {
		return nil, errDeployCredential
	}
	body, e := io.ReadAll(io.LimitReader(f, int64(max+1)))
	if e != nil || len(body) > max || unix.Fstat(fd, &after) != nil || !safe(after) || !auth.CredentialACLSafe(fd) || unix.Fstatat(parent, filepath.Base(real), &entry, unix.AT_SYMLINK_NOFOLLOW) != nil || !safe(entry) || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim || after.Mtim != entry.Mtim || after.Ctim != entry.Ctim || after.Dev != entry.Dev || after.Ino != entry.Ino || after.Size != entry.Size {
		return nil, errDeployCredential
	}
	return body, nil
}

// LoadDeploymentCredentials reads an operator-provisioned private index outside
// source/data. It never writes keys or falls back to a monitor credential.
func LoadDeploymentCredentials(path, dataDir string) (DeploymentCredentials, error) {
	body, e := privateDeploymentFile(path, dataDir, 65536)
	if e != nil {
		return nil, e
	}
	if !uniqueCredentialJSON(body) {
		return nil, errDeployCredential
	}
	var doc struct {
		Version int                                  `json:"version"`
		Targets map[string]deploymentCredentialEntry `json:"targets"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(&doc) != nil || dec.Decode(new(any)) != io.EOF || doc.Version != 1 || doc.Targets == nil || len(doc.Targets) > 100 {
		return nil, errDeployCredential
	}
	for id, entry := range doc.Targets {
		decoded, decodeErr := hex.DecodeString(entry.Binding)
		scope, scopeErr := hex.DecodeString(entry.Enrollment)
		if scopeErr != nil || len(scope) != 16 || hex.EncodeToString(scope) != entry.Enrollment || id == "" || len(id) > 128 || decodeErr != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != entry.Binding || !filepath.IsAbs(entry.TokenFile) {
			return nil, errDeployCredential
		}
	}
	return &fileDeploymentCredentials{dataDir: dataDir, path: path, digest: sha256.Sum256(body), entries: doc.Targets}, nil
}
func (s *fileDeploymentCredentials) Credential(ctx context.Context, r targetregistry.Record) (string, error) {
	if ctx.Err() != nil || r.Validate() != nil || r.HTTPSAddress == "" {
		return "", errDeployCredential
	}
	index, e := privateDeploymentFile(s.path, s.dataDir, 65536)
	if e != nil || sha256.Sum256(index) != s.digest {
		return "", errDeployCredential
	}
	entry, ok := s.entries[r.ID]
	enrollment, _ := ctx.Value(deploymentEnrollmentKey{}).(targetregistry.HistoryIncarnation)
	if !ok || enrollment == (targetregistry.HistoryIncarnation{}) || entry.Enrollment != hex.EncodeToString(enrollment[:]) || entry.Binding != targetDigest(r) {
		return "", errDeployCredential
	}
	body, e := privateDeploymentFile(entry.TokenFile, s.dataDir, 44)
	if e != nil {
		return "", e
	}
	token := strings.TrimSuffix(string(body), "\n")
	raw, e := base64.RawURLEncoding.Strict().DecodeString(token)
	if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return "", errDeployCredential
	}
	return token, nil
}
func uniqueCredentialJSON(body []byte) bool {
	d := json.NewDecoder(bytes.NewReader(body))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 8 {
			return false
		}
		token, e := d.Token()
		if e != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		if delim != '{' {
			return false
		}
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			name, ok := key.(string)
			allowed := depth == 1 || (depth == 0 && (name == "version" || name == "targets")) || (depth == 2 && (name == "binding" || name == "enrollment" || name == "token_file"))
			if e != nil || !ok || !allowed || seen[name] {
				return false
			}
			seen[name] = true
			if !value(depth + 1) {
				return false
			}
		}
		last, e := d.Token()
		return e == nil && last == json.Delim('}')
	}
	if !value(0) {
		return false
	}
	return d.Decode(new(any)) == io.EOF
}
func (h *Deployment) deploymentCredentialRevision(ctx context.Context, r targetregistry.Record) string {
	if h.credentials == nil {
		return ""
	}
	token, e := h.credentials.Credential(ctx, r)
	if e != nil {
		return ""
	}
	return deploymentRevision(ctx, r, token)
}
func deploymentRevision(ctx context.Context, r targetregistry.Record, token string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(targetDigest(r)+"\x00"+fmt.Sprint(ctx.Value(deploymentEnrollmentKey{}))+"\x00"+token)))
}
func (h *Deployment) SetDeploymentCredentials(credentials DeploymentCredentials) {
	h.credentials = credentials
}
