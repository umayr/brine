package remoteui

import "github.com/umayr/brine/internal/store"

// Only the post-preparation unsent rollback fails. Initial staging and pending
// intent writes still go through the production store.
type failedGitPreDispatchRollback struct {
	store.RemoteGitStore
	calls            int
	request, journal string
	unsent           *store.RemoteGitPolicy
}

func (f *failedGitPreDispatchRollback) SwapRemoteGit(name, source string, expected, next *store.RemoteGitPolicy) error {
	if expected != nil && next != nil && expected.Request != "" && next.Request == "" {
		f.calls++
		f.request, f.journal = expected.Request, expected.Journal
		copy := *next
		f.unsent = &copy
		return store.ErrRemoteGit
	}
	return f.RemoteGitStore.SwapRemoteGit(name, source, expected, next)
}
