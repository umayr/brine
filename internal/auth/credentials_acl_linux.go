//go:build linux

package auth

import "golang.org/x/sys/unix"

// POSIX ACL masks constrain access ACLs but default ACLs can still be inherited
// by newly created children. Reject both extended access and default ACLs, even
// if current mode bits appear private. ENODATA establishes absence; every other
// error (including an uninspectable filesystem) fails closed.
func credentialACLSafe(fd int) bool {
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		if _, err := unix.Fgetxattr(fd, name, nil); err != unix.ENODATA {
			return false
		}
	}
	return true
}
