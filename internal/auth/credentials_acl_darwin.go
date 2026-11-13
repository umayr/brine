//go:build darwin

package auth

import (
	"encoding/binary"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fgetattrlist obtains the ACL from the already-open descriptor. The Security
// xattr cannot be read with fgetxattr on Darwin. Use the native syscall so this
// check also works in CGO_ENABLED=0 builds; never reopen a path via an ACL CLI.
func credentialACLSafe(fd int) bool {
	attrs := struct {
		Count, Reserved                       uint16
		Common, Volume, Directory, File, Fork uint32
	}{Count: 5, Common: unix.ATTR_CMN_EXTENDED_SECURITY}
	var buf [8192]byte // kauth ACLs are limited to 128 24-byte entries.
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	runtime.KeepAlive(&attrs)
	runtime.KeepAlive(&buf)
	return errno == 0 && credentialDarwinACLBytesSafe(buf[:])
}

func credentialDarwinACLBytesSafe(buf []byte) bool {
	// Returned layout: uint32 total length, attrreference {int32 offset,
	// uint32 length}, referenced kauth_filesec {magic, owner/group GUIDs,
	// uint32 entrycount, uint32 flags, kauth_ace[entrycount]}.
	if len(buf) < 12 {
		return false
	}
	u32 := binary.LittleEndian.Uint32
	total := int64(u32(buf[:4]))
	if total < 12 || total > int64(len(buf)) {
		return false
	}
	offset := int64(int32(u32(buf[4:8]))) + 4 // relative to attrreference
	size := int64(u32(buf[8:12]))
	if size == 0 {
		return true
	} // no extended security record
	if size < 44 || offset < 12 || offset+size > total {
		return false
	}
	acl := buf[offset : offset+size]
	if u32(acl[:4]) != 0x012cc16d {
		return false
	} // KAUTH_FILESEC_MAGIC
	count := u32(acl[36:40])
	if count == 0xffffffff {
		return size == 44
	} // KAUTH_FILESEC_NOACL
	if count > 128 || size != 44+int64(count)*24 {
		return false
	}
	for i := uint32(0); i < count; i++ {
		flags := u32(acl[44+i*24+16 : 44+i*24+20])
		// Deny-only entries (e.g. macOS HOME's everyone deny delete) cannot
		// expand mode-bit access. Reject every grant, including inheritance-only
		// and owner grants, rather than attempting identity/rights resolution.
		if flags&0xf != 2 {
			return false
		}
	}
	return true
}
