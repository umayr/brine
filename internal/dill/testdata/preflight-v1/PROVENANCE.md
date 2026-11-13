# Frozen synthetic named-preflight v1 corpus

These 15 JSON files are byte-for-byte copies of the frozen Dill handoff corpus, not runtime output or a supported binary capability claim.

- Contract: `named-preflight-contract-v1.md`, revision 4.
- Contract SHA256: `f2cd6698c060f63626baa6b2a0971e7f6d7cad92eaf21ec2408a7fb85cdd859b`.
- Source: Dill `.pi/handoffs/preflight-wire-v1/*.json` (read-only input).
- Whole 15-file corpus SHA256: `37291da8284dbae80e118f740a356f00488232031a78feb658542594135363d6`.
- Corpus algorithm: sort filenames lexicographically; concatenate each UTF-8 basename, NUL, lowercase SHA256 hex of its original bytes, NUL; SHA256 the concatenation. This includes `manifest.json` itself. The manifest lists the other 14 files.
- Brine base: `526bb64a70e6b14720fdebe594602caf299add84` (merged S1 PR76).

All names, IDs, locations, digests and the test HMAC key are synthetic design data. The canonical-vector desired service hash is a supplied synthetic leaf, not an executed provider ServiceConfigHash proof. Test-only recomputation of canonical bytes and HMAC does not make private provider facts available to Brine, authenticate evidence, disclose real source material or qualify any target.

Accepted b3 binary pins are unchanged. Missing future preflight discovery must fail closed without fallback. A fresh candidate, independent source/binary/native qualification and a separately reviewed runtime integration remain required. No real CLI, app, engine, provisioning or paired preview/apply case has been exercised by this corpus.
