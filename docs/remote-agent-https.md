# Direct remote deployment over HTTPS

A paired target is read-only by default. Run one Dill agent per target and follow [the Dill HTTPS agent guide](https://github.com/umayr/dill/blob/main/docs/host-agent-https.md) for credentials and host-local enablement. Brine does not need SSH to pair or deploy. Use a literal private IP and port. Brine pins the leaf certificate and checks its validity period before sending the bearer token. Verify the pairing bundle out of band; it is a reusable secret. Forgetting a target does not revoke its token.

## Manual deployment and removal

1. On **Remote hosts**, pair the agent, then choose **Deploy a project** on the target. Enter a Brine project name and review the captured file list. Brine does not transfer `.git`, Brine metadata, or effective Brine variables. Git subfolders are not supported here.
2. Confirm transfer. Brine sends the exact captured bytes, checks the catalog manifest, and requests a qualified preview. A preview is not authorization and must not be treated as a sandbox: trusted Pkl runs on the target with the agent account's privileges. Snapshots can contain secrets.
3. On a write-enabled target, confirm **Create stack** or **Update stack** separately. Creation cannot adopt existing resources. An update must match the exact managed container and network IDs; it can add services but cannot omit managed services. The engine must already have the selected images. Docker has no immutable volume ID, so updates that would attach an existing named volume are refused.
4. For removal, enter the stack under **Preview removal** on the same target. The agent previews the last successfully deployed snapshot and requires the same managed identities. **Remove containers; keep data** retains the network, volumes, and snapshots. This is not a purge. The removal record prevents silently reusing the old names.

Save the request status link before confirming. A lost response is not retried; refresh the same link to observe the recorded result. A missing request or unavailable target does not prove that nothing happened. Unknown or partial effects require independent inspection rather than another apply or removal. Use disposable native Linux Docker targets to qualify the feature before relying on it.

## Remote Git auto-deploy

Under the same paired host, open **Remote auto-deploy settings** for a root-level Git-backed Brine project with local auto-deploy off. Review the target and branch and explicitly authorize that repository to deploy without individual file review. One Brine process owns the data directory. Dill never fetches Git or receives Git credentials.

Once enabled, Brine polls the configured branch about once a minute. It builds a bounded snapshot directly from the fetched commit's objects, not from the editable stack checkout. It rejects symlinks and submodules. The initial tip can create the stack; later unseen commits use the same staged manifest, preview and exact-ownership checks as manual updates. Brine records the attempted commit and request ID before sending a deployment. It does not retry an ambiguous upload or apply, or submit the same recorded commit again for that source and target. A restart looks up the same pending request. Brine retains exact attempted Git object IDs in the existing policy metadata, including across disable/re-enable for the same authorization. At 90% of its 1 MiB attempt-history budget, the settings page warns; at the limit Brine stops recording or sending new attempts rather than forgetting old commits. Brine also pauses if Dill warns that its deployment journal is near capacity. Preserve both histories; never delete one to resume automation. Disabling automation does not erase unresolved requests or remove deployed resources. Disable or reconcile automation before manual operations on that project.

### Recover a pending Git request

In **Remote auto-deploy settings**, note the exact pending request ID and the reported reason. A missing or unverified result will not clear the request. If the original journal is available, explicitly choose **Check and fence request**. Dill returns the existing status for any admitted request and does not cancel it. If Dill instead verifies that the request was never admitted, it durably forbids late admission. Brine then clears the pending request, disables automation, and retains the attempted commit in its deduplication history. Re-enable explicitly to consider future unseen commits. If the fence response is lost, Brine can observe the same durable fence by looking up that request; it does not automatically submit a fence or an apply.

Upgrade the Dill agent before deploying new Git commits: journal-bound submission requires the new agent. Legacy pending requests without a recorded journal ID can still look up a known outcome but cannot be fenced. Do not restore a stale copy of a deployment journal or use a fresh one as proof of non-admission. A full agent journal can still return existing outcomes but may not be able to record new fences.

## Verification

`DILL_AGENT_HTTPS_BIN=/absolute/dill DILL_AGENT_HTTPS_SUBCOMMAND=1 go test ./internal/agentclient -run '^TestHTTPS'` exercises the combined agent over loopback against a synthetic Docker socket. Brine handler tests cover exact transfer, explicit confirmation, Git intent persistence, and status recovery. These checks do not replace a real-engine deployment test.
