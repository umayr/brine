# Broker runtime qualification

The Dockerfile now bundles **Dill v0.13.1** with **Pkl 0.31.1**. The earlier broker-runtime baseline below used 0.12.0. Separately, `TestBrokerWebhookProductionBrowser` now qualifies the real file-provider/local-Docker operator path through the production CLI on published 0.13.0, including signed HTTPS notifications and protected-source denials; it does not claim every earlier backend/provider combination has been rerun. The paired CI workflow
verifies the released Dill binary/schema against pinned SHA-256 digests, exercises
Brine's released runner and authenticated browser fixtures, and runs an actual
native disposable Docker lifecycle. Image smoke tests verify packaged executable
versions separately. Neither packaging nor this native test makes an arbitrary
container a namespace-bound host executor; provisioning must satisfy the documented
engine/executor namespace, root, ownership and filesystem-view checks.

The historical matrix below used the connected Brine/Dill development worktrees,
Pkl **0.31.1**, Debian 13 / Linux 6.12.118 amd64,
Docker **26.1.5**, Podman **5.4.2** and RootlessKit **2.0.2** on a disposable target.
No existing daemon configuration, Docker context, stack or persistent service was
changed. Prerequisite package actions were separately authorized and audited via
`./hl`. Fixtures, failed attempts, resources and unknown outcomes are retained.

## Observed coverage

All rows below passed with process **exit 0**. File means actual allowlisted Linux
file acquisition; Vault means an actual TLS Vault **1.20.4** KV v2 backend, not a
mock. Local means the production administrator-local CLI transport; HTTPS means
the actual Dill HTTPS agent, pinned certificate/signed monitor enrollment and a
separate deploy credential/grant ceiling.

| Engine / executor | Provider | Transport | Retained sandbox log |
| --- | --- | --- | --- |
| Rootful Docker | File | Local | `file-local-10.log` |
| Rootful Docker | Vault KV v2 | Local | `vault-local-1.log` |
| Rootful Docker | File | HTTPS | `https-file-1.log` |
| Rootful Docker | Vault KV v2 | HTTPS | `https-vault-1.log` |
| Rootful Podman | File | Local | `podman-rootful-5.log` |
| Rootless Podman, private root | File | Local | `podman-rootless-private-1.log` |
| Rootless Docker, private root | File | Local | `docker-rootless-file-1.log` |
| Rootless Docker, private root | File | HTTPS | `docker-rootless-https-file-2.log` |

Each row exercises:

1. Actual Brine policy/reference state and central acquisition, captured source,
   Dill requirements, preparation and native Plan, then reviewed apply.
2. Exact provider bytes read **inside the running container**; unauthorized UID
   read and read-only write denial; no bytes in application history/state and no
   provider-source rewrite.
3. A second real update whose successful target response is deliberately discarded.
   Brine records unknown, refuses replay/new preparation, restarts, and obtains
   authoritative success by querying **the original request**. This is controlled
   response-loss injection after real apply, not evidence of a genuine outage.
4. Explicit reviewed empty-generation retirement, absent secret mount afterward,
   retained provider source/historical generations and permanent protection.

The production browser path also passed against real file acquisition and rootful
Docker: Settings metadata → Variables reference save → native Plan focus/source
consent/diff/grants → separate confirmation → shared successful History. A separate
server-side check reads the exact bytes from the real container and requires one
successful durable deployment. No replacement acquisition or target is installed.
The test transport uses TLS 1.3/mutual client authentication and explicit server
trust/pinning. Session bootstrap calls production authentication; it does not test
the standalone login/dashboard UI. HTTP responses are checked for value disclosure.
Dark/mobile 390px and light/desktop 1440px screenshots are retained.

## Rootless ownership is not an exception

The normal host filesystem root appears as an **unmapped host UID** inside the
rootless user namespace. Brine initialization correctly refuses that ownership;
never reinterpret the overflow UID as trusted root or relax ancestor checks.

The qualified fixtures instead use an explicitly operator-started, namespace-UID0-
owned private filesystem root/state, a private mount namespace and `pivot_root`,
read-only system-file mounts, and the **same actual engine/executor user namespace,
mount namespace and root view**. Each private root has a fresh operator-provisioned
machine identity. Podman enters through public `podman unshare`; Docker through
public RootlessKit. Bootstrap occurs before switching root views. `/proc` remains
the namespace bootstrap interface; no host sysctl change is required. This is an
operator execution arrangement, **not tenant/Pkl sandboxing**.

Neither Brine nor a request handler enters namespaces, escalates, creates a root
helper or delegates secret ownership to initial-host root. Grant ownership remains
mapped, private and authenticated. Child CLIs receive empty private homes rather
than ambient source/operator engine configuration or an inaccessible host-root home.
Podman broker creation explicitly selects private IPC; existing shareable/unknown
namespace evidence is still refused, never normalized into private evidence.

## Rerunnable checks and fixture contract

Run normal `make build test vet` in both development worktrees, plus Dill
`make validate` with its pinned Pkl. These and the race/paired-contract/eight
synthetic browser suites are distinct from the runtime rows above.

On an explicitly authorized disposable Linux executor, build the Brine runtime test
binary from `./internal/brokerapp`. Its private `BROKER_TEST_ROOT` must contain the
paired Linux `dill` executable, Pkl executable named `pkl`, and matching `dill.pkl`.
Preload `nginx:alpine` into the selected isolated engine; the consumer never pulls.
Set `BRINE_BROKER_RUNTIME_TEST=1` and run one case per process:

- `TestBrokerRuntimeLocalFile`
- `TestBrokerRuntimeLocalVault`
- `TestBrokerRuntimeHTTPSFile`
- `TestBrokerRuntimeHTTPSVault`
- `TestBrokerRuntimePodmanFile`

Explicit profiles additionally require operator-owned `BROKER_TEST_PROFILE` and
independently configured `BROKER_TEST_SOCKET`, and the operator must first start
the executor in the matching namespaces/root. Never use the test to enter them.
Use a fresh fixture/journal each time; tests intentionally retain resources. Do not
reset or reuse a failed/unknown request as apply authority. Run Vault cases in
separate processes because Go caches certificate roots.

The opt-in Linux browser fixture is `TestBrokerRuntimeBrowserLocalFile` in
`internal/web`; its `browser-client.crt` is a separately provisioned public client
trust anchor. Keep the client private key outside any served artifact directory.
The matching driver is `scripts/verify-broker-runtime-browser.cjs`; provide its
printed endpoint/public certificate, client certificate/key paths and Playwright
module. Set `NODE_EXTRA_CA_CERTS` to the fixture's public certificate for the client-
certificate proxy. No provider value or session credential is printed/exported.

## Explicit limits

- Infisical was subsequently deployed on a separate Linux host and verified
  through the actual Brine UI and container (see below). Its full reply-loss/restart/
  retirement lifecycle is not included in the eight-row matrix above.
- This is not the exhaustive provider × engine × transport matrix. Vault on
  Podman/rootless executors is not separately exercised. Remote Podman remains
  outside the Docker-only HTTPS agent contract.
- Real browser runtime coverage uses Workspace source. Git subfolder/automation
  guards and Restore remain unit/contract/synthetic-browser coverage, not an
  additional real Git checkout/restore runtime claim.
- The existing broad ordinary `make integration-test` suite was not run in this
  session; the eight actual paired Linux broker fixtures above are the runtime
  evidence. Infrastructure operations and their unrelated host-health gates are
  outside this repository's code/runtime verdict; no globally healthy deployment
  or production release is claimed.
- The qualified workloads use the deliberately bounded single-service static-file
  delivery slice, never-pull/no-prune policy and supported storage/namespace model.
  Do not generalize to privileged services, jobs/hooks, custom child maps, dynamic
  leases, destructive cleanup or automatic rollout.


## Separately hosted Infisical integration

An actual Infisical v0.167.16 instance with dedicated PostgreSQL/Redis and verified
private-CA HTTPS was exercised from a fresh paired Brine sandbox. Signup was closed;
the machine identity had no organization access, a viewer role on a dedicated test
project, and 300-second tokens. LAN access relied on TLS/project permissions rather
than the paid per-identity IP allowlisting feature.

The production browser path passed Settings, Variables Infisical reference save,
source/native Plan, confirmation and successful shared History. An independent
check compared the exact bytes inside the running container with the provider value
and proved viewer secret writes denied403. No credential/value was logged. This
proves actual Infisical acquisition and creation, not its full reply-loss/restart/
retirement cross-product. Operator-specific addresses, credentials, backup paths
and deployment records are intentionally kept outside this public repository.
