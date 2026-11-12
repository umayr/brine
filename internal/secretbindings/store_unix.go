//go:build unix

package secretbindings

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrUnsafe = errors.New("unsafe secret state")
var ErrStale = errors.New("secret preview is stale")

// Store owns private deployment state. Root must be an existing private
// directory outside the workspace; neither paths nor backend errors are API data.
// External same-user writers and other Brine processes require operator exclusion.
type Store struct {
	root *os.File
	path string
	mu   sync.Mutex
}

func checkDir(fd int, private bool) error {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrUnsafe
	}
	if st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
		return ErrUnsafe
	}
	if st.Mode&0022 != 0 || (private && (st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700)) {
		return ErrUnsafe
	}
	return nil
}
func openDirAt(parent int, name string, private bool) (int, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, ErrUnsafe
	}
	if checkDir(fd, private) != nil {
		unix.Close(fd)
		return -1, ErrUnsafe
	}
	return fd, nil
}
func Open(root, workspace string) (*Store, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(workspace) || filepath.Clean(root) != root || filepath.Clean(workspace) != workspace {
		return nil, ErrUnsafe
	}
	// Resolve the workspace solely to detect aliases into editable files. Root
	// itself is traversed no-follow below and must not contain symlinks.
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, ErrUnsafe
	}
	rel, err := filepath.Rel(realWorkspace, root)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, ErrUnsafe
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnsafe
	}
	if checkDir(fd, false) != nil {
		unix.Close(fd)
		return nil, ErrUnsafe
	}
	parts := strings.Split(strings.TrimPrefix(root, "/"), "/")
	for i, p := range parts {
		if p == "" || p == "." || p == ".." {
			unix.Close(fd)
			return nil, ErrUnsafe
		}
		next, e := openDirAt(fd, p, i == len(parts)-1)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return &Store{root: os.NewFile(uintptr(fd), root), path: root}, nil
}
func (s *Store) Close() error { return s.root.Close() }
func (s *Store) deployment(id string, create bool) (int, error) {
	if !namePattern.MatchString(id) {
		return -1, ErrInvalid
	}
	if create {
		if err := unix.Mkdirat(int(s.root.Fd()), id, 0700); err != nil && err != unix.EEXIST {
			return -1, ErrUnsafe
		}
	}
	return openDirAt(int(s.root.Fd()), id, true)
}
func checkFile(fd int) error {
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return ErrUnsafe
	}
	return nil
}
func readAt(dir int, name string, max int) ([]byte, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if err == unix.ENOENT {
			return nil, os.ErrNotExist
		}
		return nil, ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if checkFile(fd) != nil {
		return nil, ErrUnsafe
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil || len(b) > max {
		return nil, ErrUnsafe
	}
	return b, nil
}
func randomName() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrUnsafe
	}
	return hex.EncodeToString(b[:]), nil
}
func writeAt(dir int, name string, data []byte) error {
	// Reject unsafe preexisting targets rather than repairing or following them.
	if _, err := readAt(dir, name, 1024*1024); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafe
	}
	n, err := randomName()
	if err != nil {
		return err
	}
	n = ".tmp-" + n
	fd, err := unix.Openat(dir, n, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), n)
	defer unix.Unlinkat(dir, n, 0)
	if unix.Fchmod(fd, 0600) != nil {
		f.Close()
		return ErrUnsafe
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return ErrUnsafe
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return ErrUnsafe
	}
	if err = f.Close(); err != nil {
		return ErrUnsafe
	}
	if err = unix.Renameat(dir, n, dir, name); err != nil {
		return ErrUnsafe
	}
	if err = unix.Fsync(dir); err != nil {
		return ErrUnsafe
	}
	return nil
}
func removeAt(dir int, name string) error {
	if _, err := readAt(dir, name, 1024*1024); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if unix.Unlinkat(dir, name, 0) != nil {
		return ErrUnsafe
	}
	return nil
}
func documentBytes(data []byte, stack, target string) ([]byte, error) {
	d, err := Parse(data, stack, target)
	if err != nil {
		return nil, err
	}
	return json.Marshal(d)
}

// SaveDraft writes only validated references and invalidates any old preview.
func (s *Store) SaveDraft(id, stack, target string, data []byte) error {
	b, err := documentBytes(data, stack, target)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, true)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err = removeAt(fd, "preview.json"); err != nil {
		return err
	}
	return writeAt(fd, "draft.json", b)
}
func (s *Store) readDocument(id, stack, target, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	b, err := readAt(fd, name, MaxDocument)
	if err != nil {
		return nil, err
	}
	return documentBytes(b, stack, target)
}
func (s *Store) Draft(id, stack, target string) ([]byte, error) {
	return s.readDocument(id, stack, target, "draft.json")
}
func (s *Store) Active(id, stack, target string) ([]byte, error) {
	return s.readDocument(id, stack, target, "active.json")
}

type preview struct {
	Candidate json.RawMessage `json:"candidate"`
	Proof     string          `json:"proof"`
	Plan      []byte          `json:"plan"`
	Exit      int             `json:"exit"`
}

func validProof(p string) bool {
	b, err := base64.RawURLEncoding.DecodeString(p)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == p
}
func validatePreview(p preview, stack, target string) (preview, error) {
	b, err := documentBytes(p.Candidate, stack, target)
	if err != nil || !validProof(p.Proof) || len(p.Plan) == 0 || len(p.Plan) > 256*1024 || !json.Valid(p.Plan) || (p.Exit != 0 && p.Exit != 1) {
		return preview{}, ErrInvalid
	}
	p.Candidate = b
	return p, nil
}

func parsePreview(data []byte, stack, target string) (preview, error) {
	m, err := object(data, "candidate", "proof", "plan", "exit")
	if err != nil || string(m["exit"]) == "null" {
		return preview{}, ErrInvalid
	}
	var p preview
	if json.Unmarshal(data, &p) != nil {
		return preview{}, ErrInvalid
	}
	return validatePreview(p, stack, target)
}

// PreviewSnapshot deliberately omits the private proof. It is internal state,
// not a response DTO; workflows select what to expose after authorization.
type PreviewSnapshot struct {
	Candidate []byte `json:"-"`
	Plan      []byte `json:"-"`
	Exit      int    `json:"-"`
}

// Preview retrieves the saved candidate and plan only while the draft still
// matches. The proof remains private to this store.
func (s *Store) Preview(id, stack, target string) (PreviewSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, false)
	if err != nil {
		return PreviewSnapshot{}, err
	}
	defer unix.Close(fd)
	data, err := readAt(fd, "preview.json", 512*1024)
	if err != nil {
		return PreviewSnapshot{}, ErrStale
	}
	p, err := parsePreview(data, stack, target)
	if err != nil {
		return PreviewSnapshot{}, ErrStale
	}
	draft, err := readAt(fd, "draft.json", MaxDocument)
	if err != nil || !bytes.Equal(draft, p.Candidate) {
		return PreviewSnapshot{}, ErrStale
	}
	return PreviewSnapshot{Candidate: p.Candidate, Plan: p.Plan, Exit: p.Exit}, nil
}

// SavePreview privately records the plan and exact current draft; it never
// activates a binding. Callers hold the shared deployment coordinator lock.
func (s *Store) SavePreview(id, stack, target string, candidate []byte, proof string, plan []byte, exit int) error {
	p, err := validatePreview(preview{Candidate: candidate, Proof: proof, Plan: plan, Exit: exit}, stack, target)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, false)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	draft, err := readAt(fd, "draft.json", MaxDocument)
	if err != nil || !bytes.Equal(draft, p.Candidate) {
		return ErrStale
	}
	b, err := json.Marshal(p)
	if err != nil {
		return ErrInvalid
	}
	return writeAt(fd, "preview.json", b)
}

// InvalidatePreview discards an approval after a failed or changed plan.
func (s *Store) InvalidatePreview(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, false)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return removeAt(fd, "preview.json")
}

// ConfirmPreview requires freshly recomputed proof and plan, and activates only
// the privately saved candidate, never the caller's draft. It does not run Dill.
func (s *Store) ConfirmPreview(id, stack, target, proof string, plan []byte, exit int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, false)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	data, err := readAt(fd, "preview.json", 512*1024)
	if err != nil {
		return ErrStale
	}
	p, err := parsePreview(data, stack, target)
	if err != nil {
		_ = removeAt(fd, "preview.json")
		return ErrStale
	}
	draft, err := readAt(fd, "draft.json", MaxDocument)
	if err != nil || !bytes.Equal(draft, p.Candidate) || !validProof(proof) || subtle.ConstantTimeCompare([]byte(proof), []byte(p.Proof)) != 1 || !bytes.Equal(plan, p.Plan) || exit != p.Exit {
		_ = removeAt(fd, "preview.json")
		return ErrStale
	}
	// Marker is monotonic: a failed activation must never classify a deployment as ordinary.
	if err = writeAt(fd, "secret-enabled", []byte("1\n")); err != nil {
		return err
	}
	if err = writeAt(fd, "active.json", p.Candidate); err != nil {
		return err
	}
	return removeAt(fd, "preview.json")
}

// SecretEnabled is monotonic deployment routing metadata, not mount evidence.
func (s *Store) SecretEnabled(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, false)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	b, err := readAt(fd, "secret-enabled", 2)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || string(b) != "1\n" {
		return false, ErrUnsafe
	}
	return true, nil
}

// ProofKey returns the private 32-byte per-deployment HMAC key, creating it once.
func (s *Store) ProofKey(id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fd, err := s.deployment(id, true)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	b, err := readAt(fd, "proof-key", 32)
	if errors.Is(err, os.ErrNotExist) {
		b = make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return nil, ErrUnsafe
		}
		if err = writeAt(fd, "proof-key", b); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, ErrUnsafe
	}
	return b, nil
}
