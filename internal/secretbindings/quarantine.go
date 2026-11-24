package secretbindings

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const quarantineLimit = 4 << 20
const ledgerName = "quarantine.json"
const anchorName = "quarantine.anchor"

type Quarantine struct{ root, boot string }

// NewQuarantine requires an existing private directory. The directory and its
// anchor must remain trusted; destruction of both is outside this guarantee.
func NewQuarantine(root string) (*Quarantine, error) {
	return openQuarantine(root, true)
}

// OpenQuarantine never provisions missing state during operator recovery.
func OpenQuarantine(root string) (*Quarantine, error) {
	return openQuarantine(root, false)
}

func openQuarantine(root string, initialize bool) (*Quarantine, error) {
	boot, err := bootIdentity()
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(boot))
	q := &Quarantine{root: root, boot: hex.EncodeToString(digest[:16])}
	err = q.locked(func(fd int) error {
		a, ae := readQuarantineFile(fd, anchorName)
		_, le := readQuarantineFile(fd, ledgerName)
		if errors.Is(ae, unix.ENOENT) && errors.Is(le, unix.ENOENT) {
			if !initialize {
				return errors.New("quarantine state is not initialized")
			}
			// An independent directory marks provisioning even if both files vanish.
			if err := unix.Mkdirat(fd, "quarantine.initialized", 0700); err != nil {
				return err
			}
			if err := unix.Fsync(fd); err != nil {
				return err
			}
			f, err := unix.Openat(fd, anchorName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
			if err != nil {
				return err
			}
			file := os.NewFile(uintptr(f), anchorName)
			_, err = file.WriteString("quarantine-v1\n")
			if err == nil {
				err = file.Sync()
			}
			if e := file.Close(); err == nil {
				err = e
			}
			if err != nil {
				return err
			}
			if err = unix.Fsync(fd); err != nil {
				return err
			}
			return saveQuarantine(fd, map[string]string{})
		}
		if ae != nil {
			return ae
		}
		if string(a) != "quarantine-v1\n" {
			return errors.New("invalid quarantine anchor")
		}
		if le != nil {
			return le
		}
		_, err := loadQuarantine(fd)
		return err
	})
	if err != nil {
		return nil, err
	}
	return q, nil
}

func (q *Quarantine) locked(fn func(int) error) error {
	fd, err := unix.Open(q.root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) {
		return errors.New("quarantine directory is not private")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return fn(fd)
}
func readQuarantineFile(fd int, name string) ([]byte, error) {
	n, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(n), name)
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(n, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Size > quarantineLimit || st.Mode&0077 != 0 || st.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("invalid quarantine file")
	}
	b, err := io.ReadAll(io.LimitReader(f, quarantineLimit+1))
	if err == nil && len(b) > quarantineLimit {
		err = errors.New("quarantine file too large")
	}
	return b, err
}
func loadQuarantine(fd int) (map[string]string, error) {
	marker, err := unix.Openat(fd, "quarantine.initialized", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	unix.Close(marker)
	a, err := readQuarantineFile(fd, anchorName)
	if err != nil {
		return nil, err
	}
	if string(a) != "quarantine-v1\n" {
		return nil, errors.New("invalid quarantine anchor")
	}
	b, err := readQuarantineFile(fd, ledgerName)
	if err != nil {
		return nil, err
	}
	// Reject duplicate keys as well as malformed and noncanonical tokens.
	d := json.NewDecoder(strings.NewReader(string(b)))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("invalid quarantine ledger")
	}
	m := map[string]string{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return nil, e
		}
		key, ok := k.(string)
		if !ok || key == "" {
			return nil, errors.New("invalid deployment")
		}
		if _, ok = m[key]; ok {
			return nil, errors.New("duplicate deployment")
		}
		var v string
		if e = d.Decode(&v); e != nil {
			return nil, e
		}
		raw, e := hex.DecodeString(v)
		if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != v {
			return nil, errors.New("invalid token")
		}
		m[key] = v
		if len(m) > 4096 {
			return nil, errors.New("too many entries")
		}
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing ledger data")
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(b, canonical) {
		return nil, errors.New("noncanonical quarantine ledger")
	}
	return m, nil
}
func saveQuarantine(fd int, m map[string]string) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > quarantineLimit {
		return errors.New("ledger too large")
	}
	var r [16]byte
	if _, err = rand.Read(r[:]); err != nil {
		return err
	}
	name := ".quarantine-" + hex.EncodeToString(r[:])
	n, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(fd, name, 0)
	f := os.NewFile(uintptr(n), name)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = unix.Renameat(fd, name, fd, ledgerName); err != nil {
		return err
	}
	return unix.Fsync(fd)
}
func (q *Quarantine) List() (map[string]string, error) {
	var m map[string]string
	err := q.locked(func(fd int) error { var e error; m, e = loadQuarantine(fd); return e })
	return m, err
}
func (q *Quarantine) Check(deployment string) error {
	if deployment == "" || !utf8.ValidString(deployment) {
		return errors.New("invalid deployment identity")
	}
	m, err := q.List()
	if err != nil {
		return err
	}
	if _, ok := m[deployment]; ok {
		return fmt.Errorf("deployment %q quarantined", deployment)
	}
	return nil
}
func (q *Quarantine) Begin(deployment string) (string, error) {
	if deployment == "" || !utf8.ValidString(deployment) {
		return "", errors.New("invalid deployment identity")
	}
	var token string
	err := q.locked(func(fd int) error {
		m, e := loadQuarantine(fd)
		if e != nil {
			return e
		}
		if _, ok := m[deployment]; ok {
			return errors.New("deployment quarantined")
		}
		if len(m) >= 4096 {
			return errors.New("quarantine full")
		}
		var r [16]byte
		if _, e = rand.Read(r[:]); e != nil {
			return e
		}
		token = q.boot + hex.EncodeToString(r[:])
		m[deployment] = token
		return saveQuarantine(fd, m)
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// RecoverAfterReboot requires the same trusted local state root, never a copied
// ledger from another execution host. Inspection is explicit; no operation runs.
func (q *Quarantine) RecoverAfterReboot(deployment, token string, inspected bool) error {
	if !inspected || len(token) != 64 || token[:32] == q.boot {
		return errors.New("verified cleanup or execution-host reboot and target inspection required")
	}
	return q.Complete(deployment, token)
}

func (q *Quarantine) Complete(deployment, token string) error {
	if deployment == "" || !utf8.ValidString(deployment) {
		return errors.New("invalid deployment identity")
	}
	return q.locked(func(fd int) error {
		m, e := loadQuarantine(fd)
		if e != nil {
			return e
		}
		if token == "" || m[deployment] != token {
			return errors.New("quarantine token mismatch")
		}
		delete(m, deployment)
		return saveQuarantine(fd, m)
	})
}
