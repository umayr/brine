package webhooks

import (
	"os"
	"path/filepath"
)

// OpenExisting never interprets lost state as first-time provisioning. Production
// startup uses this API; explicit offline initialization uses Initialize.
func OpenExisting(cfg Config) (*Service, error) {
	for _, name := range []string{"webhooks.db", "webhooks.lock"} {
		info, e := os.Lstat(filepath.Join(cfg.Directory, name))
		if e != nil || !info.Mode().IsRegular() {
			return nil, errStorage
		}
	}
	return Open(cfg)
}
func Initialize(cfg Config) error {
	for _, name := range []string{"webhooks.db", "webhooks.lock"} {
		if _, e := os.Lstat(filepath.Join(cfg.Directory, name)); !os.IsNotExist(e) {
			return ErrConflict
		}
	}
	svc, e := Open(cfg)
	if e != nil {
		return e
	}
	return svc.Close()
}
