# Proposed next check for attended local review

Brine #5 — design draft, not a runtime contract or activation decision.

The [protected-source check](../scripts/check-protected-source.sh) shows that
pinned Dill validates a protected synthetic export instead of an edited
workspace. The shell fixture writes the export itself. It does not exercise
Brine's snapshot builder or tie the materialized files to a Brine review.

## One non-executing slice

Extend that evidence with a synthetic fixture that captures an entrypoint and a
relative import using `projectsnapshot.Build`. Publish the returned bytes, not
copies read from the workspace after capture. Keep provisioning trusted and
separate from the unprivileged reader. The fixture should:

- Check the protected export's entrypoint and complete file set against the
  capture, including relative paths, lengths and byte hashes. Reject extra,
  missing or changed files before invoking validation. A matching file count or
  a second hash generated from the exported files alone is not sufficient.
- Change the original import after capture and use the edited workspace as a
  control. The pinned validator must report the stack defined by the captured
  import when given the protected entrypoint, not the stack from the workspace.
- Check protection for the reader across the call, including the ancestor paths,
  and recheck the exported file set after validation. Do not claim that a
  before/after hash by itself proves stability or that a synthetic relative
  import establishes a general Pkl source policy.

Use only `dill validate` with synthetic inputs. No engine, named preflight,
apply, new route, or deployment-policy change belongs in this slice.

## What still blocks a controller

`projectreview.Store` binds its captured bytes and exact `Current` to a
session-scoped, expiring confirmation. `Current.Target` is asserted by a trusted
caller; it is not an attestation, and `Current` has no typed provider selection.
Dill's preflight input digest is not a manifest of the captured files. A future
controller needs a protected, per-request export tied to the original stack,
independently established target evidence, and a review record that binds the
provider selection to that export and to the single-use confirmation. It must
not use the live workspace or legacy Plan as authorization. None of those
runtime interfaces or placement choices are decided by this proposal.
