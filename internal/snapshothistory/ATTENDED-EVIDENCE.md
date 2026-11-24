# Unmounted attended evidence

The archive can hold an immutable original intent, an optional pre-send
uncertainty marker, and one authenticated terminal observation. These are
separate records:

- `local-intent-only`: no marker or receipt is present. This is local evidence,
  not a provider response or authority to send after restart.
- `delivery-unknown`: a marker was recorded before one live send could happen.
  It is not proof a request reached Dill. A later transport error, missing
  status, or authentic non-terminal status cannot become a receipt.
- `verified-receipt`: a trusted caller supplied a terminal observation matching
  the original authenticated intent. This describes a past operation, not
  current health, data, restore eligibility, or permission to send anything.

Reading and appending require the independently retained exact archive open
binding. A terminal observation must separately correlate target, local scope,
original credential authority revision, journal, request ID, and stack. The
original attempt and request are not the operation ID. Store/object/type-bound
encryption covers the marker and receipt; no plaintext status index is written.
Provenance (checked journal apply or original-journal status), contract version
and observation time are stored separately from the original submission intent.
Receipts are append-once; another response cannot replace them. A contradictory
reply must be surfaced by the caller, not rewritten into the archive.

Dill lead coordination verified source semantics at `d2636ecfe6d4b22f9055537b36468bd00cb12aad`
(v0.9.0): capture the original managed preview journal. For status, Brine must
send the stored journal even though the provider accepts an omitted header, and
require one exact journal echo plus original request/stack. For checked apply,
the server requires the exact original journal before admission. The body does
not attest to target, generation, authority, journal, attempt, or manifest.
Only succeeded/complete or failed/none or partial with a canonical `op_` ID
are admitted as terminal observations by this slice. Running/unknown, missing
operation ID, fence tombstones, 404/409/auth/transport failures are not receipts;
partial does not authorize follow-on writes. The journal-wide status grant is
separate from apply/fence, has no signed discovery or Brine enrollment binding,
and does not establish continuity after rotation. No endpoint or adapter is
wired to these methods. The storage code cannot independently validate a network
exchange or mint authority. It accepts only a sanitized in-process projection
from a future qualified caller; generated fixtures do not implement one.

Sidecars consume sealed bytes and directory entries, not independent operation
slots. Retained evidence pending names also consume bytes and entries. If an
archive reaches the byte or entry cap before the marker, no receipt-enabled
sender may proceed. Capacity or uncertain retention after a send must not be
reported as a persisted receipt or lead to replay. Committed evidence is
validated on archive open; malformed or orphaned evidence fails closed.
Publication uses the same sync and no-clobber path as the archive. File fixtures
prove publication return states, not power-loss durability.
