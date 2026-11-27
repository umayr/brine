# Central broker integration

Brine bundles Dill v0.12.0 and Pkl 0.31.1. Broker execution requires explicit operator provisioning. See [operator policy](broker-operator-policy.md) and [runtime qualification](broker-runtime-qualification.md) for setup and scoped evidence.

## Operator workflow

Plan reviews captured source before authenticated transfer, shows native Dill differences and grants, then requires separate one-shot confirmation. Unresolved requests allow status-only lookup in shared History. Web inputs include bounded effective non-secret configuration variables in the immutable captured input. Git subfolders use anchored workspace-descriptor capture.

Remote-Git automation is independent: only disabled, resolved policy permits manual review of the current local checkout. Enabled or pending/unknown policies remain protected; this flow neither fetches a branch nor enables rollout.

## Dispatch and recovery

Hold the stack/source deployment lock before one-shot dispatch or reference-only apply. Policy fences and revision pins remain held through transport; binding writers are excluded. Policy writers use ChangePolicy with immutable swap. Callbacks must not reenter binding state, mutate policy or close Manager. Keep deadlines bounded; shutdown cancels acquisition, waits for dispatch pins and clears preparations.

Persist exact target, journal, reference, source and request ID as durable intent before apply. Preserve separately observed outcome: post-callback storage/fence errors do not prove absence of target effects. Unknown requests are reconciled by status for the same request, never resubmitted automatically. See the policy reference for durable record requirements.

## Explicit retirement

Remove secret declarations/mounts from retained services and their Variables references. Plan discovers the empty requirement set and prepares metadata-only retirement without provider acquisition. Confirmation identifies retirement and retains native diff. Local `retire` or HTTPS `broker-retire` authority is required in addition to review/apply grants.

Predecessors come only from durable successful/partial intents for the same runtime group. Dill verifies active MAC-bound generations, current managed journal predecessor and exhaustive live workload coverage. Omitted secret-using workloads block retirement. Empty bindings never remove protection or enable ordinary deploy. Unresolved requests block preparation; retirement uses the same fences, one-shot confirmation, intent-before-apply and status-only reconciliation. History retains metadata/outcomes; no automatic cleanup deletes generations, values, volumes or protection records.

Ordinary remote operations and Git automation admission serialize with permanent registrations. Registered stacks cannot use ordinary paths even with empty bindings or removed provider policy. Original-request status, fencing/recovery and disabling automation remain available. A failed final fence is unavailable/unknown, not buffered success. Pkl is operator-trusted, not sandboxed; broker execution requires pre-existing never-pull images and a retained qualified target.

## Source-only Restore

Restore is authenticated, reviewed and stack-scoped source replacement; preserve prior source and append a new revision; never deploy, push Git, restore volumes/databases/variables/secret stores, or infer restorable files from a receipt.
