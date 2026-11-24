# Local process quarantine

Before starting a local Dill command, Brine persists an intent for its stack. An unconfirmed termination or uncertain intent completion leaves conflicting local work blocked across Brine restarts. Unknown means effects may have occurred; never replay to discover the outcome.

Linux and macOS supervise the owned process group, retaining the unreaped leader while signalling it. Capacity is eight active or retained supervisors; exhaustion rejects before process launch. If group signalling is refused, leader exit alone does not release the slot: the supervisor retains the unreaped leader and retries group cleanup before reaping. Reclaiming a slot never clears the durable unknown intent. This is not containment of descendants that escape their process group. Owned pipe copies are joined before returning output ownership; callers must provide finite stdin and bounded writers.

## Operator recovery

Recovery is **reboot-only**. Eventual process exit does not clear an intent. A Brine/container restart is not an execution-host reboot. No PID-based or operator-attestation-only override exists.

On the original execution host, using the original private data root, list intents:

```sh
brine --data-dir /absolute/original/data --process-quarantine=list
```

The JSON maps deployment IDs to exact intent tokens. IDs are `deployment-` plus the first 32 hex characters of SHA-256 of the stack name. Retain the matching token, inspect target effects, and arrange an execution-host reboot through your normal maintenance procedure. Brine does not reboot the host for you. Then explicitly clear that intent:

```sh
brine --data-dir /absolute/original/data --process-quarantine=recover \
  --quarantine-stack STACK --quarantine-token EXACT_SAVED_TOKEN \
  --quarantine-inspected --quarantine-same-host
```

Both confirmations are mandatory. The command checks boot-session change and exact-token identity; stale tokens cannot clear successor intents. Clearance is not a successful deployment result and does not run or replay any operation. These local operator commands use filesystem ownership/permissions, never start the HTTP server or engine, and never initialize missing quarantine state.

Do not copy, relocate, roll back or replace the data root to recover. Boot evidence assumes the original execution host; the same-host flag is an operator confirmation, not machine attestation. Missing, malformed or insecure state requires investigation, not deletion/reinitialization. Destruction of the entire trusted root and all initialization anchors is outside the loss-detection guarantee. Remote manual deployment recovery is separate and remains journal-bound.
