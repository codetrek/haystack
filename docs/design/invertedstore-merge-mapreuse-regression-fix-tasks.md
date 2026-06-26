# Task breakdown — C.4 merge map-reuse regression fix

Status: APPROVED (stage 4 review converged — zero Blocking/Major). Drives the APPROVED spec
`invertedstore-merge-mapreuse-regression-fix-spec.md`. Implementation is WORKFLOW-driven (stage 5),
one item at a time, each reviewed to zero Blocker/Major before commit (AGENTS.md Principle 0).

## TDD note — what "red → green" means for a behavior-preserving perf fix

This fix changes WHERE the `adds`/`dels` maps are allocated, not the merged OUTPUT. A unit test
therefore cannot go red on the buggy code and green on the fix — correctness is identical on both.
So the discipline maps as:

- **Unit level = characterization (green-stays-green).** The new test (T1) documents the merge
  output under the exact "one high-cardinality keyword + many tiny keywords" shape and MUST pass on
  BOTH the pre-fix and post-fix tree. Its job is to (a) fill a real coverage gap and (b) prove the
  revert preserves behavior. It is explicitly NOT a perf-regression guard (spec §7.2/§7.3).
- **Perf level = the real red → green.** The `idxbench` full-`lx` build is the failing measurement:
  ~277 s (RED) before the fix, ~46–50 s (GREEN) after. Recorded manually (spec §7.4), not a CI test.

No fabricated failing unit test. The build benchmark is the objective pass/fail signal for the fix.

## Tasks (ordered)

### T0 — Baseline (pre-flight, no code change)
- Confirm the current tree (`905888b`) full suite is green: `cd core && GOWORK=off go test ./invertedstore/`
  and `-race`.
- Record the RED build number: `idxbench` full-`lx` build on `/workspace` = ~277 s (already measured;
  re-confirm one number so the A/B is same-session). Note `disk=`, `hits=`, peak RSS.
- Verifiable: suite green; one baseline build line captured.

### T1 — Characterization test (green on the CURRENT/buggy tree)
- Add `core/invertedstore/merge_highcardinality_test.go` (name by behavior, not ticket id), with **TWO
  independent sub-cases / stores** — a covering merge compacts everything to ONE segment, so you cannot
  run a tiered merge after a covering one in the same store:
  - **Tiered sub-case:** build ≥ `Fanout` segments where ONE keyword has a large posting list (e.g.
    20–50k docids) and MANY other keywords have 1–2 docids each, the high-cardinality keyword flanked
    by tiny keywords on BOTH sides (so the drain hits a huge map then tiny maps); include some
    cross-source re-adds AND tombstones on the big keyword (so newest-wins + dels are exercised); run
    `mergeOneLevelForTest`; assert the merged segment's per-keyword adds/dels equal the reference.
  - **Covering sub-case:** same shape but ALSO put tombstones on the big keyword + a fully-tombstoned
    tiny keyword (so covering's "drop all dels, drop zero-add keys" path runs — else it degenerates to
    the tiered assertion); run `coveringMergeForTest`; assert adds (covering drops dels) + that the
    fully-tombstoned key is gone. Follow the pattern of `TestMerge_CoveringReclaimsTombstonesAndDuplicates`.
- **Real seams to use (verified to exist):** store ctor `newMergeStore(t, fanout)` / `newMergeStoreOpts`;
  keyword gen `kwf(prefix, n)`; record builders `addPostingForTest` / `tombstoneForTest`; spill via
  `forceSpill` (→ `spillForTest`); merge drivers `mergeOneLevelForTest` / `coveringMergeForTest`; and the
  read-back oracle **`segInvRecords(seg, tbl)`** which returns per-keyword `{adds, dels []int64}` — this
  is the load-bearing assertion seam (do NOT use a `Search`-only presence check; it would miss dels).
- **Reference model (pin these 3 rules; mirror `merge.go:296-325` exactly):** (1) within one source,
  process adds THEN dels so a del overwrites an add for the same docid; (2) across sources, the LATER
  (newer, higher-id) source wins; (3) **covering** drops ALL dels and drops a keyword with zero surviving
  adds; **tiered** keeps both adds and dels and never drops a keyword. Replay each source's add/del
  streams under these rules, then compare to `segInvRecords`.
- Do NOT add any wall-clock/`AllocsPerRun` assertion or any production hook (`segInvRecords` reads the
  sealed segment; it does not instrument the merge).
- MUST pass on the current tree (characterization) and under `-race`.
- Verifiable: `go test -run TestMerge_HighCardinality ./invertedstore/` green BEFORE any merge.go change;
  `-race` green.

### T2 — The revert (the implementation; spec §4)
- In `core/invertedstore/merge.go` `mergeSegments`: delete the hoisted `adds`/`dels` declarations AND
  the stale C.4 comment above them; in the INVERTED branch declare `adds`/`dels` fresh per key
  (insertion point: at the TOP of the `else // INVERTED` block, where the second stale C.4 comment +
  the two `clear()` calls currently are — so the branch body references freshly-declared maps), delete
  the two `clear(adds)`/`clear(dels)` calls AND the second stale C.4 comment above them; KEEP
  `var enc encodeScratch` and the `enc.encodeForwardInto`/`enc.encodeInvertedValueInto` call sites.
- ADD a concise guard comment at the fresh declaration: WHY the maps must NOT be hoisted+`clear()`-reused
  (`clear()` retains bucket capacity → O(numKeys × peak) drain; see this spec).
- Scope guard: `git diff` MUST touch ONLY `merge.go` and ONLY those lines; segment.go/codec.go/keys.go
  (C.2/C.3) untouched.
- Verifiable: full suite + T1 test + `-race` all green; `gofmt`/`go vet` clean.

### T3 — Perf A/B (the real red → green; recorded, not a CI test)
- Re-run `idxbench` full-`lx` build on `/workspace` post-fix. Expect ~46–50 s (GREEN) vs T0's ~277 s.
- Record peak RSS on BOTH the cold build AND a covering-merge pass; confirm `disk=` and `hits=` identical
  to T0, and search latency/`hits=` unchanged (reader path untouched).
- If RSS regresses materially vs ~484 MiB → STOP, escalate to spec Option D (do not improvise).
  "Materially" = peak build RSS > ~560 MiB (≈ +15 %) OR the covering-merge-pass RSS exceeds the cold-build
  RSS by more than the size of one big keyword's posting list; below that, the per-key fresh-map churn is
  noise and the fix stands.
- Verifiable: build-time line ~46–50 s; RSS/disk/hits/search numbers captured for the PR + memory.

### T4 — Gates + review + commit (workflow-owned)
- `go-cov` ≥ 90 % for `invertedstore` (core path): `cd core && go-cov ...` per the project gate.
- Multi-agent review of the DIFF (correctness + scope + test-quality lenses); LOOP fix→re-review until
  zero Blocker/Major.
- Commit ONLY after clean, with the measured A/B numbers in the message. Credit Claude + Happy.
- Verifiable: review round zero Blocker/Major; coverage gate passes; one commit.

## Ordering rationale & independence
- T0 before T1 (need the green baseline + RED build number first).
- T1 before T2 (characterization must be shown green on the buggy tree FIRST, so we know it pins
  behavior the revert then preserves — the only honest ordering for a behavior-preserving fix).
- T2 before T3 (measure the fix's effect after it lands).
- T4 last (gates/review/commit gate the whole item).
- Each task has a concrete pass/fail signal; T1 and T2 are independently checkable (test green pre-change;
  suite green post-change).

## Out of scope (per spec §8)
- Option C (sorted k-way merge), the H `+8`→`+4` spill-cadence tweak — separate follow-ups.

## Review log

### Round 1 (2 fresh agents: TDD/ordering lens / test-design+helpers lens)

- **TDD/ordering lens — VERDICT clean.** Confirmed the "no fabricated red unit test; the build benchmark
  IS the red→green" framing is honest and sound (the O(numKeys×peak) property is only observable via
  wall-time or a rejected hot-path counter), the T0→T4 ordering is correct, and "T1 green-before/green-after"
  is the honest analogue of red→green (not ceremony). All findings Minor/Nit; most actionable: name
  `segInvRecords` and quantify T3's "materially". → folded in.
- **Test-design/helpers lens — zero Blocking/Major; several Minor (folded in).** (1) T1 mis-stated tiered
  AND covering "in one flow" — covering compacts to ONE segment → **fixed** (T1 now TWO sub-cases/stores).
  (2) Helper names off → **fixed** (named the verified seams: `newMergeStore`/`newMergeStoreOpts`, `kwf`,
  `addPostingForTest`/`tombstoneForTest`, `forceSpill`, `mergeOneLevelForTest`/`coveringMergeForTest`,
  `segInvRecords`). (3) T2 insertion point unstated → **fixed**. (4) newest-wins oracle under-specified →
  **fixed** (3 rules pinned to `merge.go:296-325`). (5) covering sub-case could degenerate → **fixed**
  (tombstones required). Confirmed the revert compiles (no shadowing/unused import) and the scope-guard
  file list is correct.

**Convergence:** Round 1 returned **zero Blocking and zero Major**; all Minor/Nit findings applied. Per the
loop rule the breakdown is converged. **Status → APPROVED for implementation (stage 5, workflow-driven).**
