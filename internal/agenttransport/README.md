# Experimental pinned SSH transport

`Open(ctx, Config)` establishes one authenticated SSH connection and a local
listener. After the first local client connects, it opens one
`direct-streamlocal@openssh.com` channel to the specified remote Unix socket;
a failed channel open cancels the context rather than making `Open` fail.
Pass `forward.Context()` and `forward.Socket()` to `agentclient.New`, and
`defer forward.Close()`. The forward accepts at most one local connection;
failures require a new forward and a new agent client. `Close` returns any
filesystem removal errors, including after automatic teardown; callers should
check it. No enrollment, identity
binding, SSH agent, default keys, known-hosts lookup, command execution or TCP
forwarding is provided. `HostPublicKey` must be the independently enrolled,
canonical authorized-key form without comment or newline. A mismatch fails
before public-key authentication. Encrypted private keys are not supported.

The existing local directory must be owned by the current user and inaccessible
to group and others. The key must be an owned, private regular file, with no
final symlink. Ancestors of both paths must be trusted against replacement,
and other processes running as the same user are trusted. On Linux and macOS,
operators must independently verify ACLs and effective access for the key,
local directory, and their ancestors: mode-bit checks do not verify that ACLs
are absent or private. The transport creates
a private temporary subdirectory and a mode-0600 socket; Unix socket path
length limits apply. The remote socket's permissions and the SSH server's
forwarding restrictions are operator responsibilities.

The context is cancelled when the SSH connection, local connection, or active
channel fails, or on teardown. There is no remote channel before local accept.
`Timeout` bounds setup, deferred channel opening and each blocked proxy write;
a blocked write closes the entire forward. A local disconnect during a stalled
deferred channel open is not observed until the open returns or its timeout
expires, so cancellation can be delayed by up to `Timeout`. A peer's channel closure behind
buffered data is observable only once the data is read or a blocked write times
out. Network partitions without a detected TCP failure and idle, silent peers
are not immediately detectable; context liveness is not a health check. Queued
parallel local connections may connect before the listener closes but are never
forwarded. No real SSH host or engine has been qualified by these tests.
