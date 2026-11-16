package dillserve

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sshConfig is internal to the supervised transport.
// The known-hosts file and all directories containing it must be trusted and
// protected against modification by untrusted users throughout the connection.
type sshConfig struct {
	User, Host, KnownHosts, IdentityFile, RemoteSocket string
	Port                                               uint16
}

var (
	sshUser = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,63}$`)
	sshHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,252}$`)
	sshPath = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)
)

// safeSSHPath deliberately excludes whitespace, colon, percent expansion,
// backslashes and SSH option syntax. The conservative limit also accommodates
// platforms with a 104-byte sockaddr_un path limit.
func safeSSHFile(p string) bool {
	return len(p) <= 1024 && sshPath.MatchString(p) && filepath.Clean(p) == p && p != "/"
}

func safeSSHPath(p string) bool { return len(p) <= 90 && safeSSHFile(p) }

// sshArgs only constructs arguments. It does not start a tunnel and must not
// be used without a supervisor which proves readiness and owns cleanup.
func sshArgs(c sshConfig, localSocket string) ([]string, error) {
	if !sshUser.MatchString(c.User) || !sshHost.MatchString(c.Host) || strings.Contains(c.Host, "..") || c.Port == 0 || !safeSSHFile(c.KnownHosts) || !safeSSHFile(c.IdentityFile) || !safeSSHPath(c.RemoteSocket) || !safeSSHPath(localSocket) {
		return nil, ErrUnavailable
	}
	for _, path := range []string{c.KnownHosts, c.IdentityFile} {
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, ErrUnavailable
		}
	}
	return []string{
		"-F", "/dev/null", "-N", "-T",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + c.KnownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null", "-o", "UpdateHostKeys=no",
		"-o", "VerifyHostKeyDNS=no", "-o", "CheckHostIP=no",
		"-o", "ProxyCommand=none", "-o", "ProxyJump=none",
		"-o", "PermitLocalCommand=no", "-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "ExitOnForwardFailure=yes", "-o", "StreamLocalBindUnlink=no",
		"-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "IdentityAgent=none", "-o", "IdentitiesOnly=yes",
		"-o", "PreferredAuthentications=publickey", "-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no", "-o", "GSSAPIAuthentication=no",
		"-o", "HostbasedAuthentication=no", "-o", "Tunnel=no", "-o", "RemoteCommand=none",
		"-i", c.IdentityFile, "-l", c.User, "-p", fmt.Sprint(c.Port),
		"-L", localSocket + ":" + c.RemoteSocket, c.Host,
	}, nil
}
