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

Once enabled, Brine polls the configured branch about once a minute. It builds a bounded snapshot directly from the fetched commit's objects, not from the editable stack checkout. It rejects symlinks and submodules. The initial tip can create the stack; later unseen commits use the same staged manifest, preview and exact-ownership checks as manual updates. Brine records the attempted commit and request ID before sending a deployment. It does not retry an ambiguous upload or apply, or submit the same recorded commit again for that source and target. A restart looks up the same pending request. A bound of 128 recorded commits fails closed instead of forgetting old attempts. Disabling automation does not erase unresolved requests or remove deployed resources. Disable or reconcile automation before manual operations on that project.

## Verification

`DILL_AGENT_HTTPS_BIN=/absolute/dill DILL_AGENT_HTTPS_SUBCOMMAND=1 go test ./internal/agentclient -run '^TestHTTPS'` exercises the combined agent over loopback against a synthetic Docker socket. Brine handler tests cover exact transfer, explicit confirmation, Git intent persistence, and status recovery. These checks do not replace a real-engine deployment test.
