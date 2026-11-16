# SSH Unix-socket transport contract

`NewSSH` is a dormant, read-only transport, not connected to UI or deployment.
It supports Linux, macOS, FreeBSD, OpenBSD and NetBSD; it is not available on
Windows. The absolute `/usr/bin/ssh` executable and operating system are trusted.
No shell or inherited environment is used. OpenSSH must support the options and
Unix-socket forwarding used here; compatibility has not been tested against a
real SSH server. The operator supplies the user, DNS-style host, port,
known_hosts file, noninteractive private key and remote Unix socket path.
Encrypted keys requiring a prompt and interactive authentication are unsupported.
The operator must protect both files and their parent directories from
untrusted changes for the entire connection: the preliminary regular-file check
cannot prevent replacement races. OpenSSH verifies the host key strictly against
that explicit file; SSH host-key verification authenticates the SSH host, **not**
the service or engine. The trusted remote account, its socket permissions, SSH
server, and same-UID local processes remain in the trust boundary.

Startup readiness has a ten-second deadline (or a shorter caller deadline),
but termination and cleanup are not bounded as described below. The caller's
context controls the entire tunnel lifetime. Startup returns only after the
forwarded endpoint responds with a valid v1 health document; this proves the
path responds, not service identity or engine provenance. A private unique 0700
directory is created under `/tmp`. Paths use a conservative 90-byte limit and
exclude whitespace, percent, colon and other SSH syntax; IPv6 literals are not
supported. Unexpected process exit or cancellation makes subsequent calls
unavailable; calls in progress are cancelled. `Close` requests termination and
waits for process exit and cleanup; it may be called repeatedly or concurrently.
Kill and removal errors are ignored, so cleanup is best-effort and `Close` or a
failed startup can wait indefinitely if the process does not exit. No local
fallback, agent forwarding, proxy commands, local commands, X11 or other
forwarding is configured. Failures are intentionally surfaced as
`ErrUnavailable`; diagnostics from SSH are discarded.

## Explicit host-key binding primitive

`NewPinnedSSH` is a separate dormant API; it does not change `NewSSH` or
compare a project review. The dormant `projectstaging.NewPinnedSSH` sender binds
its actual pinned transport settings to the current review; it is not connected
to the UI. A trusted caller must independently obtain the explicit public key
and expected fingerprint and verify the target before using the primitive.
A fingerprint in `projectreview` alone is only an assertion.
The primitive accepts only a canonical, comment-free `ssh-ed25519` public key
and its exact unpadded OpenSSH `SHA256:` fingerprint. It parses the SSH wire
format and hashes the entire encoded key; host certificates, CAs and other key
algorithms are rejected. Host input is restricted to lowercase ASCII label
syntax (no trailing dot or wildcard syntax); common IP literals and all-numeric
dotted forms are rejected. This is a syntax check, not a DNS identity check:
short names, search domains, CNAMEs, hosts-file aliases and alternate numeric
address forms such as `0x7f000001` may be resolved by the operating system.
The operator must review the exact supplied hostname and understand the local
resolver; the key pin is checked for that supplied name, not for any name to
which it resolves. Ports are explicit.

A fresh 0700 tunnel directory contains a 0600 known_hosts file with exactly one
host/port/key entry for the tunnel lifetime. With configuration disabled, strict
host checking, this sole user file, an empty global file, DNS verification and
host-key updates disabled, and `HostKeyAlgorithms=ssh-ed25519`, OpenSSH must
accept precisely that raw host key or fail. No TOFU or ambient host-key fallback
is intended. This guarantee depends on a trustworthy OpenSSH implementation
supporting these options, the OS, `/tmp`, and protection against same-UID or
privileged modification; an unsupported option fails SSH startup. The identity
file and its parents also remain trusted. Host-key pinning does not authenticate
the forwarded service or validate the review, remote engine, or snapshot.
As with the original transport, cleanup is attempted after child exit and on
startup failures; removal and termination errors are ignored and waiting for
exit is unbounded. Abrupt termination of the parent or OS may also leave private
temporary files behind. No key rotation or host certificate support is
provided.

Tests use a fake process and local Unix HTTP listener, not SSH or network.
The dormant reviewed sender transfers captured project bytes only after explicit
warning that they may contain secrets. It does not scan or authorize content,
and transfer does not permit execution or deployment. Callers must hold a
writer-excluding configuration lease across each entire review and staging call,
including submission and return. There is no remote UI integration or service
authentication. A lost submission outcome is unknown and must not be retried
automatically; there is no transport fallback.
