# Brine

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
- **Auth**: single-admin login with sessions, CSRF protection, and a bearer token for API clients.
- **Engine alignment**: refuses lifecycle actions when a stack declares a different engine
  than the one Brine is connected to.

## Requirements

- Go 1.26+ to build from source
- Node.js 22+ and npm to build the web assets (`make build` does this for you)
- A `dill` binary (v0.2.0 or newer) on `PATH`, or pass `--dill /path/to/dill`
- Pkl (Dill can download it with checksum verification, or set `DILL_PKL_PATH`)
- Docker or Podman with an accessible socket
- `git` for Git-backed stacks

## Quick start

```sh
make build
./bin/brine --dill /path/to/dill
```

On first start Brine generates an admin password and API token, prints them once, and
writes them to `~/.brine/credentials.json` (mode `0600`):

```sh
cat ~/.brine/credentials.json
```

Open <http://localhost:8080> and sign in. To set credentials explicitly:

```sh
BRINE_AUTH_PASSWORD='choose-something' ./bin/brine --dill /path/to/dill
```

## Docker

The image bundles Brine, Dill (v0.2.0), and Pkl (0.31.1):

```sh
docker build -t brine .
docker run -d --name brine \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v brine-data:/data \
  -e BRINE_AUTH_PASSWORD='choose-something' \
  brine
```

Check `docker logs brine` for the generated credentials if you did not set a password.

For Podman, mount its socket and override the engine flags:

```sh
docker run -d --name brine \
  -p 8080:8080 \
  -v /run/user/1000/podman/podman.sock:/run/podman/podman.sock \
  -v brine-data:/data \
  -e BRINE_AUTH_PASSWORD='choose-something' \
  brine --engine podman --engine-socket unix:///run/podman/podman.sock
```

Build args: `DILL_VERSION` (default `0.2.0`), `PKL_VERSION` (default `0.31.1`),
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
- Brine does not terminate TLS. Put it behind a reverse proxy and add
  `--secure-cookies` to the `ExecStart` line.

`--no-auth` disables authentication and CSRF. Use it only on localhost during
development; it is explicitly unsafe on a network.

## Configuration

| Flag | Environment | Default | Description |
|---|---|---|---|
| `--port` | | `8080` | HTTP listen port |
| `--data-dir` | | `~/.brine` | Stack files, recipes, credentials |
| `--engine` | | `docker` | `docker` or `podman` |
| `--engine-socket` | | auto | Engine host/socket, e.g. `unix:///var/run/docker.sock` |
| `--dill` | | `dill` | Path to the Dill binary |
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

## Data directory

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

## Stacks

Stacks amend the published Dill schema:

```pkl
amends "package://pkg.ushd.ca/dill@0.2.0#/dill.pkl"

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

Git-backed stacks support branch, subfolder, entrypoint, read-only, and auto-deploy
with a poll interval. Auto-deploy runs `dill up` only when the pull changed `HEAD`;
uncommitted local edits are skipped rather than clobbered.

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
| `POST` | `/api/stacks/{name}/git/push` | Commit and push (non-read-only) |
| `GET` | `/api/containers` | All engine containers |
| `GET` | `/api/containers/{id}` | Inspect |
| `POST` | `/api/containers/{id}/{start,stop,restart}` | Control |
| `GET` | `/api/containers/{id}/logs` | SSE log stream |
| `GET` | `/api/images` | List images |
| `POST` | `/api/images/pull` | Pull (SSE progress) |
| `DELETE` | `/api/images/{id}` | Remove |
| `GET/POST` | `/api/recipes` | List or create snippets |
| `GET/PUT/PATCH/DELETE` | `/api/recipes/{slug}` | Manage a snippet |

Deleting a stack accepts `{"remove_containers": true}` to run `dill down` first.

## Development

```sh
make run          # build web assets + binary, run with --dev
make test
make test-race
make vet
```

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
