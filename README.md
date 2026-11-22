# Brine

> **Disabled features:** Alpha profiles, qualified operations, operation history options,
> and strict secret workflows are disabled. Existing Alpha documentation below is
> historical and does not describe currently available functionality. Brine retains
> existing secret state and its downgrade/unknown-history safety gates; disabling
> these features does not make previously or possibly secret-enabled stacks safe for
> legacy operations. Do not delete state to bypass these protections.

See the [roadmap](ROADMAP.md) for Brine's control-plane boundaries, the shared
Dill contracts, and phased cross-repository implementation tickets. Planned
capabilities are explicitly separate from the features documented below.

[![CI](https://github.com/umayr/brine/actions/workflows/ci.yml/badge.svg)](https://github.com/umayr/brine/actions/workflows/ci.yml)

Brine is a small, self-hosted web control plane for [Dill](https://github.com/umayr/dill).
It gives Dill a UI, a REST API, a Pkl editor, and Git-backed stacks without turning
your homelab into a Kubernetes cluster.

Dill remains the source of truth for configuration and lifecycle. Brine stores stack
files, renders the UI, and shells out to the `dill` binary for deploy/teardown/plan;
it talks to the container engine directly for container and image views.

## Features

- **Stacks**: create, edit, validate, format, plan, deploy, stop, restart, and tear down.
- **Pkl editor**: CodeMirror with Pkl syntax highlighting, snippets, validate, and format.
- **Plan before deploy**: field-level diff of desired vs live state.
- **Multi-file stacks**: entrypoint selection and additional `.pkl` files.
- **Git-backed stacks**: clone a repo/folder, pull, push (non-read-only), and optional
  auto-deploy polling.
- **Containers**: list, inspect, start/stop/restart, and stream logs over SSE.
- **Images**: list, pull, and remove.
- **Recipes**: reusable Pkl snippets available in the editor.
- **Appearance settings**: browser-local theme, accent and table density, with live previews
  and reset under Settings → Appearance (`/settings`).
- **Auth**: single-admin login with sessions, CSRF protection, and a bearer token for API clients.
- **Engine alignment**: refuses lifecycle actions when a stack declares a different engine
  than the one Brine is connected to.

[S1 private request/receipt history](docs/alpha-operation-history.md) is historical;
`--operation-state-dir` is disabled, and native app qualification was **NOT RUN**.

## Configuration variables

For local stacks, use **Variables** in the sidebar for values common to
stacks. Each stack page shows inherited values and lets you add stack-specific
values. Stack values win over shared values with the same name. These are **non-secret configuration**:
values are stored on disk and can appear in Pkl output, plans, diagnostics, and
container inspection. Do not store credentials or other sensitive values here.

Names are uppercase letters, digits, and underscores, beginning with a letter.
Enter the short name `APP_DOMAIN` in the UI, and read the namespaced value in Pkl:

```pkl
local domain = read("env:BRINE_VAR_APP_DOMAIN")
local logLevel = read?("env:BRINE_VAR_LOG_LEVEL") ?? "info"
// Example use: ["PUBLIC_URL"] = "https://\(domain)"
```

Variables are passed only to stack-scoped legacy Dill invocations; they are not
added to Brine's own environment. Qualified Alpha and strict secret operations
do not receive them and the variable editor is disabled in those modes. Saving
or deleting a variable does not redeploy: the next operation uses the current
values. A change between plan and deploy can change the outcome; re-plan after
any variable change. Background Git auto-deploy, when enabled for local stacks,
uses current values. Global variables are visible to every legacy stack; they are
not an isolation or authorization mechanism. Back up the private Brine data
directory if these settings must be retained. One Brine process must own a data
directory; cross-process variable writes are not coordinated.

## Requirements

- Go 1.26+ to build from source
- Node.js 22+ and npm to build the web assets (`make build` does this for you)
- A `dill` binary (v0.8.0) on `PATH`, or pass `--dill /path/to/dill`.
  Older binaries that cannot accept explicit stack/engine/socket flags are
  rejected for lifecycle and validation; Brine does not retry against defaults.
- Alpha file-secret mode is disabled. Its historical
  [qualification status](docs/alpha-secrets-status.md) records platform and testing limits.
- Pkl (Dill can download it with checksum verification, or set `DILL_PKL_PATH`)
- Docker or Podman with an accessible socket
- `git` for Git-backed stacks

## Quick start

```sh
make build
./bin/brine --dill /path/to/dill
```

On first start Brine generates an admin password and API token and writes them
to `~/.brine/credentials.json` (mode `0600`); they are not printed to logs:

```sh
cat ~/.brine/credentials.json
```

Open <http://localhost:8080> and sign in. To set credentials explicitly:

```sh
BRINE_AUTH_PASSWORD='choose-something' ./bin/brine --dill /path/to/dill
```

## Docker

Tagged builds are published to `ghcr.io/umayr/brine` for Linux amd64 and
arm64. Use the tag or digest from the [GitHub release](https://github.com/umayr/brine/releases)
rather than a floating `latest` tag. A new Brine image updates its bundled
Dill binary, not the Dill agents installed on remote hosts.

The image bundles Brine, Dill (v0.8.1), and Pkl (0.31.1):

```sh
docker build -t brine .
docker run -d --name brine \
  -p 127.0.0.1:8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v brine-data:/data \
  -e BRINE_AUTH_PASSWORD='choose-something' \
  brine --bind 0.0.0.0
```

The container must bind its internal interface; publish only to host loopback.
Read generated credentials from the private data volume, not container logs.

For Podman, mount its socket and override the engine flags:

```sh
docker run -d --name brine \
  -p 127.0.0.1:8080:8080 \
  -v /run/user/1000/podman/podman.sock:/run/podman/podman.sock \
  -v brine-data:/data \
  -e BRINE_AUTH_PASSWORD='choose-something' \
  brine --bind 0.0.0.0 --engine podman --engine-socket unix:///run/podman/podman.sock
```

Build args: `DILL_VERSION` (default `0.8.1`), `PKL_VERSION` (default `0.31.1`),
`VERSION` (binary version string).

The container runs as root because it needs the Docker socket. Treat access to the
socket as root access to the host.

## systemd

```sh
sudo useradd --system --home /var/lib/brine --shell /usr/sbin/nologin brine
sudo usermod -aG docker brine

sudo install -m 0755 bin/brine /usr/local/bin/brine
sudo install -m 0755 /path/to/dill /usr/local/bin/dill
sudo install -m 0755 /path/to/pkl  /usr/local/bin/pkl

sudo install -m 0644 packaging/brine.service /etc/systemd/system/brine.service
sudo install -d -m 0750 /etc/brine
sudo install -m 0600 packaging/brine.env.example /etc/brine/brine.env
sudo editor /etc/brine/brine.env   # set BRINE_AUTH_PASSWORD

sudo systemctl daemon-reload
sudo systemctl enable --now brine
sudo systemctl status brine
```

`StateDirectory=brine` creates `/var/lib/brine` with the right ownership. If you use
Podman instead, adjust `User`, `Group`, `ExecStart`, and the socket in the unit.

## Authentication

- Browser sessions use an `HttpOnly`, `SameSite=Lax` cookie. State-changing requests
  require a CSRF token (the UI adds it automatically).
- API clients use `Authorization: Bearer <api-token>` and do not need CSRF.
- The API token is in `credentials.json`:

  ```sh
  TOKEN=$(jq -r .api_token ~/.brine/credentials.json)
  curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/stacks
  ```

- There is no RBAC: one admin account, intended for a single operator.
- Sessions live in memory; restarting Brine signs everyone out.
- Login admits 20 attempts per client IP in a fixed five-minute window, including
  successful attempts; success does not reset the window. Limits are per process
  and reset on restart. Password checks are globally bounded; overload can return
  HTTP 503. Brine uses the direct peer address and does not trust forwarded IP
  headers. Behind a reverse proxy, all clients sharing its address share one login
  budget and can lock each other out; restrict access at the proxy and apply
  appropriate per-client rate limits there. Multiple Brine instances do not share
  limits, so distributed clients and requests spread across instances can multiply
  the aggregate attempt rate. If the 10,000-address table is full of live entries,
  new addresses are rejected until an entry expires; live limits are never evicted
  to admit a newcomer. Expired slots are reclaimed on demand. A distributed
  flood can therefore deny login to new addresses for as long as the table stays
  full; enforce network-level abuse controls at the proxy or other network boundary.
- Brine does not terminate TLS. Put it behind a reverse proxy and add
  `--secure-cookies` to the `ExecStart` line.

`--no-auth` disables authentication and CSRF. Use it only on localhost during
development; it is explicitly unsafe on a network. Never publish or proxy a
no-auth instance, including through a reverse proxy bound to a LAN interface.
Loopback binding and browser-origin checks are defense in depth, not authentication;
local processes and non-browser clients can still control it.

## Configuration

| Flag | Environment | Default | Description |
|---|---|---|---|
| `--port` | | `8080` | HTTP listen port |
| `--bind` | | `127.0.0.1` | Literal listen IP; no-auth requires loopback |
| `--data-dir` | | `~/.brine` | Stack files, recipes, credentials |
| `--engine` | | `docker` | `docker` or `podman` |
| `--profile` | | `legacy` | Only `legacy` is supported; `alpha` is rejected. See the historical [target boundary](docs/alpha-target-contract.md). |
| `--engine-socket` | | auto | Engine host/socket, e.g. `unix:///var/run/docker.sock` |
| `--dill` | | `dill` | Operator-selected Dill binary for local execution |
| `--staging-socket` | | unset | Opt-in absolute local dill-serve Unix socket; requires `--staging-identity` and authentication |
| `--staging-identity` | | unset | Stable operator-asserted local staging target identifier; not receiver attestation |
| `--remote-enabled` | | `false` | Opt in to authenticated remote UI; deployment requires explicit host opt-in |
| `--remote-registry-dir` | | unset | Explicit initialized private remote registry directory |
| `--remote-forward-dir` | | unset | Existing private forwarding directory, only for SSH and SSH reveal |
| `--remote-timeout` | | unset | Explicit positive remote connection timeout |
| `--init-remote-registry` | | `false` | Initialize a new registry and exit; does not start or enable the server |
| `--coordination-state-dir`, `--coordination-layout`, `--coordination-daemon-id` | | unset | Disabled; explicitly passing any of these flags is rejected, even with an empty value. |
| `--operation-state-dir` | | unset | Disabled; explicitly passing this flag is rejected, even with an empty value. |
| `--secret-target`, `--secret-policy`, `--secret-pkl` | | unset | Disabled; explicitly passing any of these flags is rejected, even with an empty value. |
| `--auth-user` | `BRINE_AUTH_USER` | `admin` | Admin username |
| `--auth-password` | `BRINE_AUTH_PASSWORD` | generated | Admin password |
| `--api-token` | `BRINE_API_TOKEN` | generated | Bearer token for API clients |
| `--no-auth` | | `false` | Disable auth (development only) |
| `--secure-cookies` | | `false` | Set `Secure` on the session cookie |
| `--session-ttl` | | `12h` | Session lifetime |
| `--request-timeout` | | `30s` | Timeout for ordinary HTTP handlers |
| `--deploy-timeout` | | `30m` | Timeout for deploy/teardown/git commands |
| `--dev` | | `false` | Re-parse templates on every request |
| `--version` | | | Print the version |

## Remote hosts

Remote targets are read-only by default. On a paired HTTPS host, the agent operator can explicitly enable staging, preview, and checked creation, updates and removal of managed stacks. The same Brine server retains its local stack and container controls, but local Git auto-deploy is paused. This is not a whole-server read-only mode.

Remote access is opt-in and requires an authenticated administrator and a private registry. For direct HTTPS, no SSH forwarding directory is needed:

```sh
brine --init-remote-registry --remote-registry-dir /private/brine-registry
brine --remote-enabled --remote-registry-dir /private/brine-registry --remote-timeout 10s
```

Open **Remote hosts**, enter the agent's private IP and port, and paste the pairing bundle exported locally by Dill. Brine verifies the TLS pin and the signed engine binding before saving the target. The bundle is a reusable credential: protect the registry and its backups. **Forget** removes the target from Brine only; rotate the token on the agent to invalidate copies. See [direct HTTPS agent](docs/remote-agent-https.md). The SSH enrollment path additionally requires `--remote-forward-dir`.

Initialization is exclusive and does not repair existing or partial state. **On any initialization I/O, sync, rollback or final-lock-close error, stop and quarantine all consumers and reconcile out of band before access or retry.** Quarantine is not automatically enforced across processes after such an error; another opener may see apparently valid state. Never delete or recreate the root or lock during use. Missing, corrupt or insecure registry state fails closed rather than starting an empty registry. See [registry initialization and filesystem assumptions](internal/targetregistry/README.md). The operator must protect and verify ACLs, ancestors and backups; filesystem rollback, whole-root removal/replacement and noncooperating writers are outside the locking guarantee. Restart signs out sessions and invalidates pending enrollment approvals.

For the proposed manual agent installation and identity-gathering procedure, see [Dill's pinned provisioning guide](https://github.com/umayr/dill/blob/5e493fdaf1b32363b4b3d620b1422be77725d280/docs/host-agent-install.md) and the [Brine enrollment field contract](docs/target-registry.md). The guide and remote SSH path are **not real-SSH qualified**; in particular, same-UID SSH access can expose other sockets and Docker access is highly privileged. Independently verify all endpoint and agent/engine identity fields and pins out of band. The SSH connection pins the supplied host key and authenticates the agent binding; it does not discover trustworthy pins or verify the local key path or reveal-verifier fingerprint. Use a dedicated private SSH key, trusted agent and isolated engine. Access to a container engine may grant host-level privileges. Inspection metadata, commands, paths, health tests and logs may contain secrets. Restrict Brine and any TLS reverse proxy to the administrator. This UI provides enrollment and reads. Environment reveal is disabled by default and requires separate explicit provisioning and opt-in; see [remote reveal](docs/remote-reveal.md). The SSH path remains read-only. The direct HTTPS project deployment flow is documented in [direct HTTPS agent](docs/remote-agent-https.md), including Brine-triggered Git auto-deploy. Shutdown cancels active remote operations and streams, then allows ten seconds for all requests to drain; on timeout it force-closes connections and reports a shutdown error. Delivered or buffered bytes cannot be recalled, and failed cleanup requires operator investigation.

## Local snapshot staging

With both staging flags configured, an authenticated browser session can prepare a
complete immutable project snapshot review, then separately confirm a one-shot
transfer to the local Unix socket. The receiver must advertise the provisional
snapshot-staging capability and match the selected stack and engine. Unix socket
access control is the trust boundary; the asserted identity does not attest the
receiver. Remote/SSH staging is unsupported. The Alpha attended web boundary
continues to deny these POSTs.

Snapshots may contain sensitive files, including ignored files and keys; the file
list is not a secret scanner. Only transfer occurs, never evaluation or deployment.
Existing secret and downgrade guards remain mandatory for any later operation.
Reviews show the captured source root and are held in memory for up to five
minutes, bounded to eight reviews and 64 MiB, and invalidated by restart.
In-app variable writes are serialized with final consent and transfer. The source
root is not a filesystem sandbox: external writers and trusted ancestors remain
outside that coordination. Variable and session lease waits are cancellable.
Staging admission is limited to eight requests: the shared legacy stack lock
itself cannot be interrupted, so a cancelled waiter may remain until that lock is
released, but is checked before further work. Logout prevents a confirmation that
has not acquired its submission lease; that lease may be acquired just before the
physical POST, and once it wins, logout waits for the network operation and cannot
recall an already-started upload. Shutdown cancels staging requests and closes
the review store. It is serialized with the submission decision: shutdown winning
first prevents a POST, including during receiver negotiation; if submission wins,
cancellation cannot recall an already-started POST and its outcome may be unknown.
If transfer outcome is unknown, do not retry: inspect the receiver before
preparing another review.

## Isolated Alpha operation (historical)

**Not available:** the normal server rejects the Alpha profile and all qualified
operation, history and strict-secret options. The following describes the former
contract and safety precautions, not instructions for starting the current server.

The former opt-in [qualified operation consumer](docs/alpha-operation-consumer.md) targets
exact Dill candidate `b3ecbaa29260f65d56cf55e54ccd8fedcdf496d2`, selected explicitly
with `--dill`. Set all three coordination flags together only after independent
native Linux Docker host/state provisioning. Authenticated manual stack
up/start/restart/stop/down use the negotiated CLI contract without fallback; plan
and saved-source validation are read-only. Updating the bundled Dill does not
enable or qualify local Alpha. Teardown, rm, secrets, stdin, direct SDK writes, qualified stack
deletion and **all Alpha background Git pull/auto-deploy remain disabled**.
Current-action receipts preserve process failure separately from outcome/effects;
there is no durable history or auto-acknowledgement. **Actual native app/browser
canary has NOT RUN; qualification and #67 durable approval remain pending.**

Use a dedicated local Docker target and a new private data directory (`mkdir -m 700`),
not an existing Portainer/hl engine or state directory. Keep authentication and
CSRF enabled; bind Brine to loopback. For LAN access, retain loopback upstream and
use a correctly configured TLS reverse proxy with `--secure-cookies`, restricting
proxy access to the operator. Engine socket access is effectively host-level control.

Use only synthetic canaries, unique stack/container/volume names and unused ports;
use disposable isolated data, one operator and one writer/controller. Do not touch
existing Portainer/hl stacks, Watchtower/Autoheal labels, DNS/Caddy configuration,
or production credentials. Record an expiry and explicitly clean up only the named
Alpha resources and its private data after checking their identity. No SSO, RBAC,
remote enrollment, production binding provider, vault or late-bound secret provider
is provided.

Brine gives both legacy and strict Dill subprocesses a minimal explicit environment
(PATH/locale, isolated private HOME/XDG directories, and configured Pkl/policy paths),
not the inherited service environment or its login/API credentials. This does not
isolate Git subprocesses, which still inherit the service environment (see below).
Dill/Pkl run with the server user's filesystem privileges; credentials and stack data
are not a sandbox boundary. Errors, engine logs, streams and stored outcomes can
contain deployment values; use synthetic secrets only and protect logs/state. Do not
put login or API credentials in deployment data. Future mutation/history paths have
not been security-qualified by this Alpha slice.

## Data directory

Persisted credentials require a canonical, service-owned mode-0700 state directory
and a regular, service-owned, single-link mode-0600 credential file. Symlinks,
group/world-writable ancestors (including sticky temporary directories), and ACLs
that expand or inherit access are rejected rather than silently repaired. macOS
permits deny-only ACLs; Linux rejects extended access and default POSIX ACLs.
Provision a private state directory without inherited grants before startup; mode
bits alone do not neutralize ACLs. Uninspectable ACLs fail closed. File-backed
credential handling is supported on Linux/macOS; other platforms require explicit
credentials instead of an insecure filesystem fallback.

```
~/.brine/
  credentials.json          # 0600
  stacks/<name>/
    compose.pkl             # entrypoint (configurable)
    meta.json               # stack metadata, git status
    repo/                   # clone for git-backed stacks
  recipes/<slug>/
    body.pkl
    meta.json
```

Stack and recipe paths are validated; directories are `0700` and files `0600`.

Git stack creation uses the configured deploy timeout (five minutes when it is
non-positive), rather than the ordinary request timeout. A request cancellation
or deadline before publication stops registration and cleans that attempt's staging
area; existing stack paths are never replaced. Once publication begins, cancellation
cannot guarantee rollback. If the response is lost, check the stack name before
retrying: creation may have succeeded. A process crash can leave a private staging
directory requiring operator inspection. Git retains the service's existing
execution environment and authority.

File-backed authentication requires a canonical private data directory on Linux
or macOS: owned by the server user, mode `0700`, beneath root/server-owned
ancestors without group/other write permission. Credential files must be regular,
single-link, server-owned files with mode `0600`; symlinks, hardlinks, unsafe
parents (including temporary shared directories) and permissive existing state
are rejected before credentials are read. Brine does not chmod unrelated files
or automatically repair preexisting unsafe state. Before upgrading an existing
installation, stop Brine and inspect the exact data directory and credential
file ownership, modes and link identities; deliberately provision a private
canonical location rather than blindly chmodding a path that may be a link.
New image data volumes start private; existing volumes retain their old modes
and may require operator repair. On other platforms, supply both explicit login
password and API token; file-backed credential loading fails closed.

## Stacks

Stacks amend the published Dill schema:

```pkl
amends "package://pkg.ushd.ca/dill@0.5.0#/dill.pkl"

config {
  name = "my-app"
  engine = "docker"
  services {
    ["web"] {
      image = "nginx:alpine"
      ports = List("8080:80")
    }
  }
}
```

Keep `config.name` equal to the Brine stack name. The starter template syncs it while
you type, and the server rewrites the placeholder on create if it was left as `my-app`.

### Upgrading existing stacks to Dill 0.5

Dill 0.5 uses camelCase schema fields and resolved configuration keys. Before
running existing stacks with the new binary, update their schema import to
`package://pkg.ushd.ca/dill@0.5.0#/dill.pkl` and rename any old fields:
`host_ip` → `hostIp`, `read_only` → `readOnly`, `external_volume` → `externalVolume`,
`start_period` → `startPeriod`, `container_name` → `containerName`,
`pull_policy` → `pullPolicy`, `depends_on` → `dependsOn`,
`network_mode` → `networkMode`, and `cap_add` → `capAdd`.

Brine does not rewrite existing stack files, recipes, or Git repositories.
Validate the migrated project and review its plan before deploying. These Pkl
schema changes do not rename Brine's own REST API fields or Git settings.

Git-backed stacks support branch, subfolder, entrypoint, and read-only settings.
Local Git auto-deploy is paused until reviewed-candidate apply is available.
Saved preferences and intervals are preserved. Pull explicitly, review the
changes, and deploy manually. Remote HTTPS Git policy is separate.

## API

All routes are under `/api` and require authentication (except `/api/health`).

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/health` | Liveness check (public) |
| `GET/POST` | `/api/stacks` | List or create stacks |
| `POST` | `/api/stacks/git` | Create a stack from a Git repository |
| `GET/PUT/DELETE` | `/api/stacks/{name}` | Read, update, or delete a stack |
| `POST` | `/api/stacks/{name}/{up,down,stop,start,restart,teardown}` | Lifecycle |
| `GET` | `/api/stacks/{name}/plan` | Desired-vs-live plan |
| `GET` | `/api/stacks/{name}/validate` | Validate the entrypoint |
| `GET` | `/api/stacks/{name}/containers` | Containers by `dill.stack` label |
| `GET/POST` | `/api/stacks/{name}/files` | List or create stack files |
| `GET/PUT/DELETE` | `/api/stacks/{name}/files/{filename}` | Read, write, or delete a file |
| `POST` | `/api/stacks/{name}/entrypoint` | Change the entrypoint |
| `GET/PATCH` | `/api/stacks/{name}/git` | Git settings |
| `POST` | `/api/stacks/{name}/git/pull` | Pull |
| `POST` | `/api/stacks/{name}/git/push` | Commit and push (non-read-only); optional `{"message":"…"}` |
| `GET` | `/api/containers` | All engine containers |
| `GET` | `/api/containers/{id}` | Inspect |
| `POST` | `/api/containers/{id}/{start,stop,restart}` | Control |
| `GET` | `/api/containers/{id}/logs` | SSE log stream |
| `GET` | `/api/images` | List images |
| `POST` | `/api/images/pull` | Pull (SSE progress) |
| `DELETE` | `/api/images/{id}` | Remove |
| `GET/POST` | `/api/recipes` | List or create snippets |
| `GET/PUT/PATCH/DELETE` | `/api/recipes/{slug}` | Manage a snippet |

Git push uses `chore: update stack via brine` for an omitted, whitespace-only, or empty object body, or an empty `message`. Otherwise the body must contain exactly one string `message` and fit within 64 KiB. Invalid bodies return `400` before Git runs on an otherwise eligible stack.

For local stacks, deleting a stack accepts `{"remove_containers": true}` to run `dill down` first. Teardown accepts `{"force": true}` to force image removal. Where the action is available, each endpoint accepts an omitted or whitespace-only body, `{}`, or its one Boolean option. Invalid bodies (including unknown or duplicate keys) return `400` without changing the stack. Configured qualified Alpha denies both deletion variants to preserve source/recovery context; use explicit Down and inspect its receipt instead.

## Development

```sh
make run          # build web assets + binary, run with --dev
make test
make test-race
make vet

# Standalone component contracts (no engine or Dill required)
npm ci
npm run build:web
npm run test:components
```

### Browser regression harness

The tracked fixture and Playwright suite provide browser regression checks locally
and in CI. Install dependencies with `npm ci` first.

```sh
npx playwright install chromium
npm run test:ui
```

To use an existing Chromium-compatible browser instead, set
`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` to its executable before running the UI tests.
The suite runs source/component contracts and browser checks against automatically
managed disposable fixtures on localhost ports 18082 and 18083 (the latter tests
synthetic sign-in). These ports must be free; existing servers are not reused. The managed fixtures never use a real Dill or engine. Screenshots are saved
under `test-results/`; these checks do not run lifecycle actions. To use a separately
started disposable login instance, set `BRINE_VISUAL_BASE_URL` and
`BRINE_VISUAL_PASSWORD` before running `tests/visual.spec.js`.

For populated UI testing without Docker or Dill, start the read-only fixture
server from the repository root:

```sh
go run ./cmd/brine-fixture  # http://127.0.0.1:18082
# In another terminal:
BRINE_FIXTURE_BASE_URL=http://127.0.0.1:18082 npx playwright test tests/fixtures.spec.js
```

The fixture uses temporary data, binds only to localhost, blocks infrastructure
writes, and never connects to a real engine. Its optional `--auth` mode permits
only login/logout POSTs using disposable fixture credentials. It provides example
stacks, containers, images, and logs. It is intended for visual inspection, not
lifecycle integration tests. The ignored `.local/` directory is not a production runtime dependency.

### Development file reload

Run `--dev` from the repository root to read `internal/web/templates/` and
`internal/web/static/` from disk on each request. Reload the page to see edits;
rerun `npm run build:web` after changing frontend sources in `internal/web/assets/`.
Use only a trusted local source tree: development file serving follows symlinks.
Without `--dev`, templates and static assets remain embedded in the binary.

The production component layer is Brine-owned; Tailwind supplies utilities, with no
DaisyUI dependency. Customize the plain CSS variables in
`internal/web/static/theme.css` (no Tailwind rebuild for variable-only edits).
Static assets are embedded in release binaries, so shipping theme changes requires
rebuilding the Go binary. Rebuild web assets after changing
`internal/web/assets/` or the Tailwind configuration. See
[the component and theme contract](docs/ui-components.md) for classes, palettes,
status semantics, editor variables and checks.

Layout:

```
cmd/brine            CLI entrypoint and flags
internal/agent       Local (and future remote) agent abstraction
internal/api         REST handlers
internal/auth        Sessions, CSRF, bearer token, credentials
internal/dill        Dill binary runner and JSON contracts
internal/engine      Docker-compatible engine adapter
internal/git         git CLI wrapper and auto-deploy syncer
internal/httpx       Request timeouts and detached command contexts
internal/pkl         Pkl source helpers (engine/name detection)
internal/store       Filesystem stack and recipe stores
internal/web         HTMX handlers, templates, embedded assets
```

## Limitations

- One engine per Brine process; no multi-environment agents yet.
- No container stats, exec/console, volume/network pages, or prune yet.
- No registry credential UI; private images rely on engine-side auth.
- No notifications or scheduled backups.
- Single admin; no RBAC.

### Git trust boundary

Git operations run as the Brine service user, with its filesystem, network,
credentials and environment. Brine suppresses client hooks discovered through
`core.hooksPath`, including template-installed and intentionally configured
operator hooks; server-side hooks are not suppressed. This is not Git sandboxing:
Git configuration (including repository-local and operator configuration),
filesystem-monitor hooks configured via `core.fsmonitor`, credential and remote
helpers, SSH commands, filters selected by repository attributes, and transport
URLs can still execute programs or access and disclose data available to the service. Clone URLs may address local paths
or network services. Git output and errors can reach API/UI clients and sync
status. Only use trusted Git sources, repository metadata, configuration and
service credentials; do not expose Git operations to untrusted users. Isolation
of Git execution and network/credential access is required for hostile sources.
