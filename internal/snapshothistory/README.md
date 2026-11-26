# Private snapshot history (standalone v1)

The provider snapshot primitive described below is deliberately unmounted. It is **not** the [stack source archive](../../docs/source-history.md), which shares its private filesystem and encryption helpers but uses a separate identity, keys, object format and publication protocol. The provider primitive
has **no** configuration, startup, route, sender, provider, approval, restore,
retention-deletion, metrics, or Dill-engine integration. Calling it does not
make a provider request or establish that one was admitted, rejected, completed,
or has any provider operation ID.

## Explicit provisioning and lifecycle

An operator separately provisions all three absolute, symlink-free paths:

* an empty owner-only `0700` archive directory;
* an owner-only `0700` key parent containing one existing, owner-owned,
  single-link regular `0600` key file of exactly 32 raw bytes; and
* the editable workspace directory, only so the API can reject lexical nesting
  or equality with archive/key paths.

The key must be outside both archive and workspace. `Initialize` is explicit and
only writes the authenticated store-identity object to an otherwise empty
archive. It never creates, generates, chmods, repairs, replaces, backs up, or
rotates the key. `Open` only opens an already initialized archive; a missing,
wrong, substituted, damaged, unknown-version, or noncanonical identity/object
fails closed. A store identity is encrypted and authenticated even before the
first archived operation exists.

The v1 implementation is Linux-only. It traverses archive/key/workspace paths
from `/` with descriptor-relative `O_NOFOLLOW`, requires trusted non-writable
root/owner ancestors and private final archive/key directories, checks Linux
POSIX access/default ACL xattrs, owner/mode/type/single-link properties, and
holds a nonblocking exclusive `flock` on the retained archive directory. Other
platforms return `ErrUnavailable`; they do not silently approximate directory
sync or ACL behavior.

This is not a sandbox against root, a hostile filesystem, rollback, mount
replacement, or a same-UID process that ignores the API. The operator must use
one trusted local filesystem with functioning `flock`, Linux `renameat2` with
`RENAME_NOREPLACE`, same-directory atomic publication, regular-file sync and
directory `fsync`; NFS, sync-emulating
filesystems, backup/restore rollback and untrusted mount namespaces are out of
scope. ACL inspection can only prove Linux POSIX ACL xattrs are absent; trusted
path ownership remains an operator requirement.

## Binding, exactness and cryptography

`Commit` takes one caller-created opaque `ObjectID`, a byte-distinct Brine
attempt ID and provider request ID, nonempty original journal bytes, nonempty
approved target scope bytes, an independently supplied expected Dill v1 manifest
digest, and an exact `projectsnapshot.Snapshot`. It recomputes the digest solely
via `dillserve.SnapshotManifestDigest` and rejects a mismatch before writing.
It does not redact or infer any value. Under the writer lock it also rejects a
duplicate attempt ID or provider request ID within an exactly equal opaque
original journal; this is a local archive uniqueness check, not a provider
replay/admission/status assertion. It then encrypts the complete snapshot and
all intent bytes together. `Open` requires independently retained exact IDs,
journal, target scope, and manifest digest, and verifies every field before
returning cloned exact bytes. It never derives a provider operation ID, status,
or `not-admitted` claim.

Format v1 uses Go's standard AES-256-GCM with a fresh 96-bit random nonce per
sealed value. Per-object AES keys are independently derived from the externally
provisioned 32-byte master key using HKDF-SHA-256, domain-separated by version,
record kind, store identity and opaque object identity. The AEAD associated data
also binds format/version, kind, store ID and object ID. The identity object uses
a separate domain. No cipher, nonce construction, key generation, key rewrite,
or fallback cipher is hand-rolled. A failed open does not generate or replace
any key. GCM's random nonce requirement depends on the OS cryptographic random
source; objects are no-clobber, so a successful object identity cannot be
resealed by this API.

The on-disk record is a small binary header (`BSH1`, version byte, nonce,
AES-GCM ciphertext/tag) only. The JSON payload is entirely inside ciphertext;
there are no plaintext journals, snapshot bytes, target scopes, IDs, manifests,
or metadata side files. Error values intentionally contain no sensitive bytes.

## Capacity and uncertainty

Defaults are 2 GiB and 100 operations. Capacity is charged against each file's
logical apparent sealed byte length, including the authenticated identity
metadata and any retained pending/crash remnants; it is not charged against
logical snapshot length and is not a physical disk reservation. Filesystem block
and directory-allocation overhead are deliberately not guessed. Pending remnants
also consume an operation slot. The archive supports at most 4096 directory
entries, including the identity, retained objects, and pending names. Explicit
operation caps above 4095 are rejected before initialization or open; zero
selects the documented default. Each sealed object is limited to 32 MiB. The
store validates committed objects at open and before each commit. Under the
exclusive writer lock it reserves the new sealed bytes, operation, and one
name before creating a temporary file. The final rename consumes that same
entry, not a second one. Lowered caps do not delete existing readable objects:
`Open` accepts a validated archive over the new quota, while new commits reject
with `ErrCapacity`.

A commit writes only ciphertext to a private no-follow temporary file, syncs it,
then uses Linux `renameat2(RENAME_NOREPLACE)` for no-clobber atomic publication.
There is no link/unlink fallback: an unavailable syscall or unsupported
filesystem fails closed, and the temp remnant remains charged. It directory-syncs
before reporting success. There is no deletion/pruning of committed records and
no startup cleanup of failed/pending remnants. Any error after publication, including directory sync
failure, is `ErrIndeterminate`; callers must not retry, replay, or infer the
outcome. File/directory sync and reopen tests demonstrate the API boundary, not
proof of power-loss behavior on a filesystem that lies about sync completion.

## Bounded restart metadata discovery

`DiscoverRestartMetadata` is an in-process, authenticated archive scan only. A
caller must provide one nonempty exact approved-target scope and a non-nil
`RestartAuthorizer` which authorizes that exact byte value; there is no wildcard,
empty-scope, generic drop, route, network, provider, send, fence, restore, or
execution form. The scope is local intent metadata only and says nothing about
provider admission or outcome.

It decrypts and validates original ciphertext before returning a bounded page of
opaque locators, attempt IDs, provider request IDs, original journals, and
manifest digests. It returns no snapshot, source bytes, paths, per-file hashes,
or plaintext-derived index, and writes no sidecar or persistent cursor. A page
returns at most 64 matching records and authenticates at most 256 records and
2 GiB of ciphertext. When work remains, `More` is true even if the current page
has no matches. Pass `Next` unchanged; it is an encrypted scan position bound
to this archive and exact scope, not an object locator or authorization. Page
boundaries can reveal that more encrypted records were present, not their
identities or scope. Continue until `More` is false. For a stable archive this
visits every object, even above a single page's work limits. New objects added
between calls can sort before the cursor; start a fresh scan to include them.
Cancellation, invalid bounds or cursor, failed authorization, malformed root,
or corruption encountered on the current page return an error and no page.

The existing Ubuntu PR `Build and test` job runs `scripts/ci-test-go.sh`, which
provisions and validates the synthetic `/brine-go-test-home.XXXXXXXX` before
`go test -race ./...`. Archive tests require that generated HOME and recheck its
private ancestry before creating their own randomized directory beneath it.
In CI (`GITHUB_ACTIONS` or `CI` set), an absent fixture is a hard failure. Plain
local Linux test commands skip only these privileged fixtures when the generated
HOME is absent. A matching but unsafe HOME is always a failure. To exercise the
fixtures, run `bash scripts/ci-test-go.sh` on a disposable Linux runner; never use
an operator HOME or relax path checks.
