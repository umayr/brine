// Package configvars stores non-secret Pkl configuration values.
package configvars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/umayr/brine/internal/store"
	"golang.org/x/sys/unix"
)

const Prefix = "BRINE_VAR_"
const maxSize = 1 << 20

var validName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Store is shared by the UI and runner. Values are configuration, not secrets.
// A single Brine process must own a data directory.
type Store struct {
	// lease serializes all writes with staging's final resolve and submission.
	// Lock order: lease before mu. Callers must never upgrade a read lease.
	lease  chan struct{}
	mu     sync.Mutex
	dir    string
	stacks store.StackStore
}

type stackValues struct {
	CreatedAt string            `json:"created_at"`
	Values    map[string]string `json:"values"`
}

type document struct {
	Global map[string]string      `json:"global"`
	Stacks map[string]stackValues `json:"stacks"`
}

func New(dataDir string, stacks store.StackStore) (*Store, error) {
	dir := filepath.Join(dataDir, "config-variables")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("unsafe variable directory")
	}
	return &Store{dir: dir, stacks: stacks, lease: make(chan struct{}, 1)}, nil
}

func validate(name, value string) error {
	if !validName.MatchString(name) || strings.HasPrefix(name, Prefix) {
		return errors.New("name must start with an uppercase letter and contain only uppercase letters, digits and underscores; omit BRINE_VAR_")
	}
	if strings.ContainsRune(value, 0) || len(value) > 16384 {
		return errors.New("invalid or oversized value")
	}
	return nil
}

func (s *Store) read() (document, error) {
	d := document{Global: map[string]string{}, Stacks: map[string]stackValues{}}
	fd, err := unix.Open(filepath.Join(s.dir, "variables.json"), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return d, nil
	}
	if err != nil {
		return d, errors.New("variable store unavailable")
	}
	f := os.NewFile(uintptr(fd), "variables.json")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxSize {
		return d, errors.New("unsafe variable store")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil || len(b) > maxSize {
		return d, errors.New("variable store unavailable")
	}
	if err = json.Unmarshal(b, &d); err != nil || d.Global == nil || d.Stacks == nil {
		return d, errors.New("invalid variable store")
	}
	for name, value := range d.Global {
		if validate(name, value) != nil {
			return d, errors.New("invalid variable store")
		}
	}
	for stack, entry := range d.Stacks {
		if store.ValidateStackName(stack) != nil || entry.Values == nil || entry.CreatedAt == "" {
			return d, errors.New("invalid variable store")
		}
		for name, value := range entry.Values {
			if validate(name, value) != nil {
				return d, errors.New("invalid variable store")
			}
		}
	}
	return d, nil
}

func (s *Store) save(ctx context.Context, d document) error {
	b, err := json.Marshal(d)
	if err != nil || len(b) > maxSize {
		return errors.New("variable store too large")
	}
	f, err := os.CreateTemp(s.dir, ".variables-*")
	if err != nil {
		return errors.New("variable store unavailable")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("variable store unavailable")
	}
	// The rename is the mutation linearization point. Never publish a queued
	// request after its context has already been cancelled.
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(s.dir, "variables.json")); err != nil {
		return errors.New("variable store unavailable")
	}
	return nil
}

func (s *Store) identity(stack string) (string, error) {
	if s.stacks == nil {
		return "", errors.New("stack store unavailable")
	}
	item, err := s.stacks.Get(stack)
	if err != nil || item == nil || item.CreatedAt.IsZero() {
		return "", errors.New("stack identity unavailable")
	}
	return item.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), nil
}

// Values returns a copy of the selected scope; empty stack denotes global.
func (s *Store) Values(stack string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stack != "" {
		if err := store.ValidateStackName(stack); err != nil {
			return nil, err
		}
	}
	d, err := s.read()
	if err != nil {
		return nil, err
	}
	vars := d.Global
	if stack != "" {
		id, err := s.identity(stack)
		if err != nil {
			return nil, err
		}
		entry := d.Stacks[stack]
		vars = nil
		if entry.CreatedAt == id {
			vars = entry.Values
		}
	}
	out := map[string]string{}
	for k, v := range vars {
		out[k] = v
	}
	return out, nil
}

// Snapshot returns shared and stack values from the same storage snapshot.
func (s *Store) Snapshot(stack string) (map[string]string, map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := store.ValidateStackName(stack); err != nil {
		return nil, nil, err
	}
	d, err := s.read()
	if err != nil {
		return nil, nil, err
	}
	id, err := s.identity(stack)
	if err != nil {
		return nil, nil, err
	}
	shared := make(map[string]string, len(d.Global))
	for k, v := range d.Global {
		shared[k] = v
	}
	local := map[string]string{}
	if entry := d.Stacks[stack]; entry.CreatedAt == id {
		for k, v := range entry.Values {
			local[k] = v
		}
	}
	return shared, local, nil
}

// Effective takes one consistent snapshot and applies stack overrides.
func (s *Store) Effective(stack string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := store.ValidateStackName(stack); err != nil {
		return nil, err
	}
	d, err := s.read()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range d.Global {
		out[Prefix+k] = v
	}
	id, err := s.identity(stack)
	if err != nil {
		return nil, err
	}
	entry := d.Stacks[stack]
	if entry.CreatedAt == id {
		for k, v := range entry.Values {
			out[Prefix+k] = v
		}
	}
	return out, nil
}

// ReadLease prevents in-process variable writes until release. It is exclusive
// to keep both readers and writers context-cancellable without helper goroutines.
func (s *Store) ReadLease(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case s.lease <- struct{}{}:
	}
	if err := ctx.Err(); err != nil {
		<-s.lease
		return nil, err
	}
	return func() { <-s.lease }, nil
}

// Set adds or deletes one item atomically within this process.
func (s *Store) Set(stack, name, value string, remove bool) error {
	return s.SetContext(context.Background(), stack, name, value, remove, nil)
}

// SetContext waits for the variable lease before acquiring optional authority.
// Authority remains held until the mutation is published or abandoned.
func (s *Store) SetContext(ctx context.Context, stack, name, value string, remove bool, authority func(context.Context) (context.Context, func(), error)) error {
	if err := validate(name, value); err != nil {
		return err
	}
	if stack != "" {
		if err := store.ValidateStackName(stack); err != nil {
			return err
		}
	}
	release, err := s.ReadLease(ctx)
	if err != nil {
		return err
	}
	defer release()
	if authority != nil {
		var releaseAuthority func()
		ctx, releaseAuthority, err = authority(ctx)
		if err != nil {
			return err
		}
		if ctx == nil || releaseAuthority == nil {
			return errors.New("authority unavailable")
		}
		defer releaseAuthority()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.read()
	if err != nil {
		return err
	}
	vars := d.Global
	if stack != "" {
		id, err := s.identity(stack)
		if err != nil {
			return err
		}
		entry := d.Stacks[stack]
		if entry.CreatedAt != id || entry.Values == nil {
			entry = stackValues{CreatedAt: id, Values: map[string]string{}}
		}
		d.Stacks[stack] = entry
		vars = entry.Values
	}
	if remove {
		delete(vars, name)
	} else {
		vars[name] = value
	}
	if err := s.save(ctx, d); err != nil {
		return fmt.Errorf("save variables: %w", err)
	}
	return nil
}
