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
and does not establish continuity after rotation. The unmounted attended
handler requires both durable intent and a durable unknown marker before its
one checked apply. It saves a verified terminal observation only after that
apply returns; a live response is not presented as a saved receipt on archive
error. No production endpoint or grant adapter is wired to this seam. Storage
cannot independently validate a network exchange or mint authority. Generated
fixtures exercise the boundary, not a qualified provider.

Sidecars consume sealed bytes and directory entries, not independent operation
slots. Retained evidence pending names also consume bytes and entries. If an
archive reaches the byte or entry cap before the marker, no receipt-enabled
sender may proceed. Capacity or uncertain retention after a send must not be
reported as a persisted receipt or lead to replay. Committed evidence is
validated on archive open; malformed or orphaned evidence fails closed.
Publication uses the same sync and no-clobber path as the archive. File fixtures
prove publication return states, not power-loss durability.

## Current local operation guard

An attended intent publication is the local reservation. Under the same exclusive
archive writer lock and in-process mutex used for evidence publication, Commit
validates the complete retained inventory and rejects a new attended intent if
any prior intent for that exact enrollment scope and stack lacks a valid terminal
receipt with complete or no effects. This includes intent-only records, unknown
delivery, partial receipts and failed receipt persistence. Neither a different
journal nor another object's success clears a blocker. No force-clear path is
introduced. This protects cooperating writers on the same trusted archive only.

Restore uses an exact full scan (not discovery pagination), with fail-fast mutex
admission, before review and staging. It is not a reservation: a concurrent new
intent may be admitted after a scan, so the same atomic Commit guard is final
before marker and send. Full inventory work is bounded by the existing archive
entry/object limits, may be slow, and observes context between items; it is not a
hard wall-clock filesystem guarantee. Neither local quiet nor a historical
receipt establishes current provider state, ownership or grant authority.
