package targetregistry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// CommitChange stays inside the process: Records contain private credentials.
// Observers must export only explicitly allowlisted metadata.
type CommitChange struct{ Before, After *Record }
type CommitReceipt struct {
	ID   string
	Done func(error)
}
type CommitObserver func(context.Context, string, []CommitChange) (CommitReceipt, error)

// Configure before serving. Callback runs under the exclusive registry lease;
// it must not call the registry or acquire any registry lease itself.
func (r *Registry) SetCommitObserver(observer CommitObserver) {
	r.observerMu.Lock()
	defer r.observerMu.Unlock()
	r.commitObserver = observer
}

func (r *Registry) prepareCommit(ctx context.Context, d *disk) (CommitReceipt, error) {
	noop := CommitReceipt{Done: func(error) {}}
	r.observerMu.RLock()
	observe := r.commitObserver
	r.observerMu.RUnlock()
	if observe == nil {
		return noop, nil
	}
	before, e := r.load()
	if e != nil {
		return noop, e
	}
	previous := map[string]Record{}
	next := map[string]bool{}
	for _, t := range before.Targets {
		previous[t.ID] = t
	}
	changes := []CommitChange{}
	for _, t := range d.Targets {
		next[t.ID] = true
		old, ok := previous[t.ID]
		if !ok {
			copy := t
			changes = append(changes, CommitChange{After: &copy})
		} else if old != t {
			a, b := old, t
			changes = append(changes, CommitChange{Before: &a, After: &b})
		}
	}
	for _, t := range before.Targets {
		if !next[t.ID] {
			copy := t
			changes = append(changes, CommitChange{Before: &copy})
		}
	}
	if len(changes) == 0 {
		d.EventHead = before.EventHead
		return noop, nil
	}
	// A visible predecessor head is insufficient after a failed rename sync.
	// Make the directory entry durable before SQLite acknowledges its commit.
	if e = r.syncCommitDirectory(); e != nil {
		return noop, e
	}
	receipt, e := observe(ctx, before.EventHead, changes)
	if e != nil {
		return noop, e
	}
	if receipt.ID == "" {
		return noop, ErrMismatch
	}
	if receipt.Done == nil {
		receipt.Done = noop.Done
	}
	d.EventHead = receipt.ID
	return receipt, nil
}

func (r *Registry) syncCommitDirectory() error {
	if r.syncDirectory != nil {
		return r.syncDirectory()
	}
	dir, e := os.Open(r.dir)
	if e != nil {
		return e
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// JournalIdentity binds recovery to this canonical registry and lock inode,
// not merely its current config alias. Reinitialization cannot confirm or abort
// an intent belonging to an unavailable previous registry.
func (r *Registry) JournalIdentity(ctx context.Context) (id string, resultErr error) {
	f, e := r.lockFile(ctx, true, true)
	if e != nil {
		return "", e
	}
	defer func() { resultErr = errors.Join(resultErr, r.closeLock(f)) }()
	var st unix.Stat_t
	if e = unix.Fstat(int(f.Fd()), &st); e != nil {
		return "", e
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d:%d", r.dir, st.Dev, st.Ino)))
	return fmt.Sprintf("%x", sum), nil
}

// CommitHead is readable only under the same validated shared lease as normal
// registry reads. Missing/corrupt state is not an empty registry or removal.
func (r *Registry) CommitHead(ctx context.Context) (head string, resultErr error) {
	f, e := r.lockFile(ctx, true, true)
	if e != nil {
		return "", e
	}
	defer func() { resultErr = errors.Join(resultErr, r.closeLock(f)) }()
	d, e := r.load()
	if e != nil {
		return "", e
	}
	if e = r.syncCommitDirectory(); e != nil {
		return "", e
	}
	return d.EventHead, nil
}
