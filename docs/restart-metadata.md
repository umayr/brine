# Optional archive metadata discovery

The metadata-only endpoint is disabled by default. It lists authenticated intent
metadata from a previously provisioned encrypted archive; it does not read out
archived configuration, grant restore consent, submit deployments, contact Dill,
or establish the outcome of an earlier operation. Manual operation fencing is
separate. This is not a restore UI.

This integration is undergoing qualification. CLI help still marks it unavailable
pending combined review and full verification. Do not enable it on live state
based on generated-fixture tests. The bundled Dill version remains **v0.8.1**
(`Dockerfile`); this local archive reader does not establish compatibility with
newer Dill execution features.

## Startup settings

All settings are restart-only, with no automatic approval after enrollment
replacement. Authentication and remote access must already be enabled.

| Flag | Required value when enabled |
| --- | --- |
| `--restart-metadata-enabled` | Explicit opt-in |
| `--restart-metadata-dir` | Existing private encrypted archive directory |
| `--restart-metadata-key-file` | Existing private 32-byte archive key file |
| `--restart-metadata-store-id` | Independently retained nonzero 64-character lowercase hex archive identity |
| `--restart-metadata-max-bytes` | Positive maximum sealed archive bytes |
| `--restart-metadata-max-operations` | Explicit operation bound, 1–4095 |
| `--restart-metadata-enrollment` | `host=incarnation`, repeat for 1–64 exact approvals; incarnation is nonzero 32-character lowercase hex |

A host name is not an approval. The configured incarnation must match the current
registry enrollment. Forgetting and re-enrolling a target invalidates old pins,
including after restarting Brine. Missing, duplicate, stale or partially valid
approval lists make the entire optional reader unavailable. Metadata approval does
not imply permission to read configuration content or execute a deployment.

The archive must already have an authenticated identity and inventory. Startup
never initializes or repairs it, creates directories, generates keys, or prunes
objects. There is currently no operator CLI in this integration to provision an
archive or export enrollment pins. Do not fabricate identities or copy fixtures
into operational state. Keep the archive, key and editable workspace separate;
configured state overlaps and aliases are rejected. The archive/key ancestry must
satisfy the store's private ownership, permissions and no-symlink checks. Native
archive access is supported on Linux; unsupported platforms refuse it.

Archive `Open` is **not filesystem-read-only**: it takes an exclusive lock and
syncs the directory after authenticating the inventory. Another process using the
same archive prevents opening. Byte/operation bounds are not filesystem quotas.

## HTTP contract

`GET /remote/archive/metadata?host=HOST` requires an authenticated administrator
cookie session and an approved current enrollment. Bearer authentication is not
accepted. Only `host` and optional opaque `after` pagination selectors are
accepted, once each. The browser cannot supply a target scope or authorization.

The JSON page contains `Records`, `Next` and `More`. Rows contain an opaque locator,
attempt/request/journal identifiers and manifest digest, not configuration bytes.
Do not interpret these identifiers as proof of successful deployment. Use `Next`
unchanged as `after` when `More` is true, even when a page is empty. Pagination is
not a consistent snapshot across concurrent changes. Responses are non-cacheable.

The handler limits a response to 32 rows and 64 KiB, uses a two-second request/write
deadline, and retains the current enrollment lease through response flush. Mutex
contention refuses rather than queues behind archive writes. These limits do not
make filesystem I/O interruptible or establish a hard archive-scan wall-time bound.

## Startup and shutdown failure

Default-off performs no archive filesystem work. When enabled, startup opens in a
background worker; pending or failed startup returns unavailable rather than
blocking ordinary serving. Diagnostics omit paths and key material. Invalid
optional settings never fall back to legacy mutations or initialize fresh state.

Shutdown cuts off new metadata requests, cancels existing requests, waits for
admitted handlers and then closes the archive. A worker stuck in open, read or
close cannot produce a successful drain acknowledgment: the server reports a
shutdown failure after its grace period. Cancellation alone does not prove that
filesystem work has stopped. Preserve state and investigate; do not repair or
retry unknown deployment operations through this endpoint.
