# Implementation plan — invertedstore scale-correctness + Lucene-ization

Status: PROPOSAL (pre-spec roadmap). Synthesized from a 4-architect judge panel (priors:
scale-correctness-first / incremental-wins / Lucene-faithful-endstate / validation-first; all scored 8,
converged) + adversarial scoring. This is the ORDERED ROADMAP of SDD efforts, NOT a spec — each phase
below becomes its own spec → review → tasks → review → workflow-TDD per AGENTS.md. Companion to
`invertedstore-luceneization-exploration.md`.

## 0. The spine (and why this order)

Treat **no-hot-keyword-OOM + bounded-merge** as a CORRECTNESS FLOOR for a general engine (the scale
reframe: lx is a test corpus; real deployments are orders of magnitude larger). Ship the floor in
**format-neutral, no-reindex** steps FIRST, then spend the user's **single** reindex once on the deep
byte-format fix. Order = value / blast-radius, honoring build ≫ mem > search with scale-correctness as a
mem-floor.

| # | Phase | Format change | Reindex? | Bounds |
|---|---|---|---|---|
| **D0** | Synthetic scale-stress harness (HARNESS-ONLY, SDD-exempt) | none | no | — (builds the RED baseline) |
| **S1** | Streaming per-keyword MERGE reconciliation | none (byte-identical) | no | merge cross-source **union** term |
| **S2** | Streaming per-keyword SEARCH/GetDocs reconciliation | none (identical hits) | no | search **union** term |
| **S3** | P3 max-seg cap + newest-contiguous selection + livelock guard | FormatVersion in-place | no | remap, largest single merge, blast radius |
| **S4** | THE single StorageVersion reindex: P1 split + P2 delete-collapse + chunked postings + keyword-range skip | StorageVersion (one bump) | **yes (once)** | hot-keyword df **fully** (merge AND search) |

**Honest boundary (corrected an over-claim):** S1/S2 do NOT make peak resident df-independent — each
source still decodes its whole posting blob (`readExternal`), so per-source df remains until **chunked
postings (S4)**. S1/S2 remove the larger *unioned-across-K-sources* term with zero reindex; S4 closes the
residual per-source term. So the OOM floor is delivered in two installments: most of it for free (S1–S3),
the rest in the one reindex (S4).

## 1. Phases

### D0 — Synthetic scale-stress harness (HARNESS-ONLY, no SDD gate) — DO FIRST
Extend `core/cmd/idxbench` (today build+search only — no df control, no delete/reindex) with: (1) a
controllable-df corpus generator (Zipf body + injectable HOT keywords, `-hotkw N -hotdf D` pushing one
keyword to millions of postings) so the OOM vector is forced at CI-small absolute size; (2) a
delete/re-index workload phase (`-delete M% -reindex N%`) idxbench has NEVER run; (3) a low `-cap` to
force many L0 segments; (4) a deterministic per-merge / per-hot-keyword resident probe (in-process hook
à la `mergeRemapObserver` + a `HeapInuse` high-water sampler over noisy VmHWM) + a **GOMEMLIMIT survival
mode** (baseline OOMs, fix completes). **Output = the RED baseline**: peak resident scales **O(df)** on
today's merge and search. Touches NO product code → lands immediately, unblocks every downstream gate.
De-risk: if the baseline does NOT OOM at achievable df, urgency recalibrates before any spec.

### S1 — Streaming per-keyword MERGE reconciliation (format-neutral, no reindex)
Replace `merge.go`'s materialized `adds`/`dels` maps per keyword with a **k-way merge of the per-source
SORTED docid streams**, emitting in sorted order under newest-wins (later source wins; del-vs-add within
a source). Byte-identical merged output. Bounds the **cross-source union** term to O(K). Validate:
byte-identical vs the existing `refModel`/`segInvRecords` oracle (`merge_highcardinality_test.go`,
`differential_test.go`) + the D0 ratio (union term no longer scales with df). Highest value/blast-radius:
biggest merge-memory cut at the smallest radius, no reindex.

### S2 — Streaming per-keyword SEARCH/GetDocs reconciliation (format-neutral, no reindex)
Rework `search.go`'s per-source whole-slice append + cross-tier union into a streaming newest-wins union
across head+spilling+segments. Identical hit-set. Bounds the search **union** term. Validate:
differential identical results + D0 ratio. After S1 because search is the lowest priority and smaller
radius. HONEST: each segment's `scanPrefix` still hands a whole decoded posting value → full bound waits
on S4.

### S3 — P3 max-seg cap + selection rule + guards (FormatVersion in-place, no reindex)
Add `Options.MaxMergedSegmentBytes` (high provisional default 5 GiB, `0`=uncapped, floor=1 below it).
Tiered selection becomes a **size-bounded NEWEST-contiguous-by-id subset** (NOT whole-level, NOT
greedy-oldest — a merged output gets a fresh highest id and newest-wins resolves by global id descending,
so an OLD subset would invert newest-wins and **resurrect superseded postings**). A level whose smallest
Fanout members sum over-cap is **settled** (never re-selected) + a **livelock guard** in
`pickLowestQualifyingLevelLocked`. Covering stays **UNCAPPED** (it's the only del-reclaim path until P2).
Validate: deterministic synthetic segMeta fixtures (no data) — newest-contiguous selection, output ≤ cap,
no settled-level livelock, inert at lx; D0 low-cap bounded-merge resident. Cap VALUE is honestly
un-tunable now → ship mechanism + knob + provisional default.

### S4 — THE single StorageVersion reindex bundle (one bump, re-tokenize once)
Magic `SRSEG\x00\x00 → \x00\x01`, all byte-format changes together so the user reindexes AT MOST ONCE:
- **P1 split** — two segment families under ONE shared MANIFEST (a `Kind` field on `segMeta`); forward
  split out so the inverted merge is a string-keyed k-way merge that **can drop a key** → removes the
  ~120 lines of remap/ordSentinel/self-heal. At split time the forward is UNCHANGED (still ordinals).
- **P2 delete-collapse** — per-doc **forward-version tombstone** for the DELETE path (O(1) fan-out);
  keep del-postings for re-index; staleness checked against a **resident per-table version table rebuilt
  on Open** (rides `recomputeLive`); a **store-wide seal-order version sequence** (not per-doc
  read-before-write). Forward collapses to **docid→version-only** here.
- **Chunked/block postings + skip data** — the inverted VALUE becomes skip-indexed blocks + a skip-aware
  `readExternal` so neither merge nor search ever materializes a whole hot keyword → the residual
  per-source df term S1/S2 could not close; peak becomes **O(chunk), df-INDEPENDENT**.
- **Per-segment `[minKeyword,maxKeyword]` range** in segMeta (metadata, in-place) so a PREFIX Search can
  range-skip a whole segment. **NO bloom.**
Validate: round-trip + reindex-from-old-magic per byte change; differential oracle on
build+delete+reindex+search; single-atomic-MANIFEST crash property across the split
(`crash_recovery_test.go`) with `Kind`; D0 ratio → df-independent. LAST: largest blast radius, the only
re-tokenize.

## 2. Decision resolutions (from the panel)

- **Streaming-reconciliation vs chunked-postings sequencing:** streaming FIRST (format-neutral), chunked
  postings LAST (in the reindex bundle). Independent; same §8 vector at opposite blast radii.
- **Does streaming make merge "flat in df"? NO** — it bounds the cross-source UNION term; per-source
  whole-value decode stays O(df) until chunked postings. Stated honestly in S1/S2.
- **Streaming also fixes search?** Yes, as a SECOND format-neutral step (S2), but PARTIAL for the same
  reason.
- **P3 subset rule:** greedy **NEWEST-contiguous-by-id** (correctness — tombstone resurrection), NOT
  greedy-oldest. The panel's #1 load-bearing invariant.
- **P3 cap default:** mechanism on-by-default, high provisional **5 GiB** (inert at lx), `0`=uncapped,
  documented as un-tuned pending a real corpus; covering UNCAPPED; settled-level livelock guard REQUIRED.
- **Forward encoding (the §7 tension):** **DECOUPLE the split from the forward bytes** — split (P1) with
  the forward UNCHANGED, then collapse to **docid→version-only (B3)** when P2 adds versions; both inside
  the one reindex. **Reject B1-strings** (measured +78 MiB disk, #3-negative, build never reads them) and
  **B2** (relocates the ordinal complexity).
- **P2 delete form:** **(b) delete-only collapse** (per-doc forward-version tombstone for DELETE; keep
  del-postings for re-index). Reject (a) full per-posting versioning (taxes the write-once build #1, breaks
  the delta-varint layout). Staleness via a resident version table; **store-wide seal-order version
  sequence** (replay-safe), not a per-doc read-before-write counter.
- **P4 membership:** **NO bloom.** Answer exact membership from the already-sorted term-dict (binary
  search); ADD a per-segment `[minKeyword,maxKeyword]` range (metadata, in-place, no reindex) for prefix
  Search segment-skip. Revisit a real bloom only if a real corpus shows dict binary-search is the
  bottleneck.
- **MANIFEST:** one **shared** MANIFEST + a `Kind` field on segMeta (preserves the single-atomic-install
  crash story; two manifests open a torn-state window).

## 3. Validation without a large corpus (three honest tiers)

1. **Correctness-at-scale (load-bearing, validatable NOW):** a SYNTHETIC stress corpus whose **shape, not
   scale**, matters — boundedness is scale-invariant. PRIMARY gate = the **scaling-RATIO assertion**: run
   the same corpus at 2+ hot-keyword df values and assert peak resident does NOT scale with df after the
   fix; GOMEMLIMIT survival as a binary secondary (baseline OOMs, fix completes). S1/S2 assert the **union
   term** bounded; only chunked postings (S4) asserts **fully df-independent / O(chunk)**.
2. **Differential correctness:** every format-neutral item byte-identical merged output / identical Search
   hits vs the current engine (existing `refModel`/`segInvRecords`/`differential_test.go`); every reindex
   item round-trips + matches the oracle on build+delete+reindex+search.
3. **Policy/mechanism (P3 selector, settled-level, livelock, P2 liveness, MANIFEST Kind):** deterministic
   synthetic segMeta fixtures, no data volume.

**Honestly UN-measurable until a representative corpus exists** (ship as mechanism + knob + a written
"awaits a representative corpus" caveat, NEVER quoting the spike's 22s/241 MiB/+78 MiB as production): the
P3 cap byte VALUE, the chunk SIZE / skip-density crossover, whether a bloom ever beats dict-membership,
and absolute build/search throughput at scale.

## 4. Open decisions for the maintainer (each blocks a spec)

1. **Release shape:** ship the no-reindex floor (S1+S2+S3) as a FIRST release before the S4 reindex
   bundle, or hold everything for one combined release? **Rec: floor first** — real OOM relief without
   spending the reindex.
2. **P3 cap default value + chunk size:** un-measurable now. **Rec: ship provisional knobs** (5 GiB cap,
   a chosen chunk size) documented as awaiting a representative corpus.
3. **Forward-encoding direction at P1→P2:** confirm DECOUPLE (split forward-unchanged → collapse to
   version-only with P2). **Rec: yes**; re-measure B1-strings disk on the REAL tableId/int64 format only
   if you want to reconsider.
4. **Deferred §8 scale-fragility track:** the single-JSON MANIFEST rewritten O(segments) per install
   (which **P3 makes worse** — more segments) and `recomputeLive` O(docs) on Open. **Rec: defer to a
   follow-on track** after the OOM floor + reindex, but track it as a known P3 side-effect.

## 5. Key risks (carry into every spec)

- Streaming reconciliation must preserve EXACT newest-wins (add→del→add collapse, del-vs-add within a
  source, oldest→newest across sources) — gate byte-identical.
- The OOM floor is PARTIAL until S4 (per-source whole-value decode) — don't over-claim S1/S2.
- The user's ONE reindex: every byte change MUST ride the single S4 bump; no byte change may leak out
  earlier.
- P3 newest-contiguous-by-id is a CORRECTNESS rule (tombstone resurrection), not a perf knob.
- P2's resident version table MUST be fully built (Open) before any merge consults it ([I]<[F] means the
  version is unknown when a posting streams).
- Un-measurable constants (cap value, chunk size, bloom payoff) ship as documented-provisional knobs.

## 6. Search impact (holistic — analyzed up front though search work lands last)

Net: **search MEMORY ends materially better (O(chunk), df-independent peak); LATENCY ends roughly flat**,
with a real, time-boxed **regression window in S3→S4** that must be managed. Per query shape (verified
against `search.go`):

- **Hot / common-prefix:** end-state = **MEMORY win** (chunked postings → O(chunk) decode buffers vs
  today's whole-`readExternal`), **LATENCY neutral** — range-skip does ~nothing (a common prefix is in
  nearly every segment) and total per-source decode CPU is unchanged.
- **Rare / selective-prefix:** end-state **better** — the per-segment `[minKeyword,maxKeyword]` range-skip
  culls non-overlapping segments at one string-compare each (the direct offset to S3's raised K).
- **Absent keyword:** end-state **best** — range-skip (0 I/O) + dict-binary-search membership.
- **AND-intersection:** **NEUTRAL — gets nothing as scoped.** The engine runs each AND term's full
  `Search` independently (`engine.go:186,197`) and intersects fully-materialized per-term maps; the store
  never sees the other terms, so the chunked-posting "skip-to-relevant-docids" leapfrog is **unreachable**
  without a NEW store-side multi-term entrypoint. In the **S3 interim** AND is the **worst-hit** (the O(K)
  segment multiplier stacks per term).
- **Deleted-doc resolution — the headline coupling (see decision below).**

### CRITICAL: the P2 delete model and search are a TRADE, not free either way

The plan's "P2(b) O(1) delete fan-out" and "search resolves deletes free, inline" are **in conflict** —
you cannot have both:
- **Today** a delete writes a per-keyword del-posting for every old keyword (`update.go` `tombstonePosting`
  fan-out); Search resolves it **inline + free** via the inverted value's dels-half (`search.go:149-153`),
  never touching the forward.
- **To actually win on the delete WRITE side (O(1) fan-out)** you must STOP writing per-keyword
  del-postings on delete → then Search must **filter every candidate result** against a resident
  deleted-docid structure (a roaring bitmap of dense int64 docids; O(1)/result + new resident RAM + the
  union may carry not-yet-reclaimed dead docids until a merge drops them). That is a **search-side cost**.
- **To keep search free** you must keep the del-postings on delete → then the delete write is NOT O(1)
  (no write win) and P2(b) only buys merge-reclaim/re-index, not cheaper deletes.

So **P2 is a conscious trade**: cheap deletes (write) ⇄ a search-time liveness filter (read). The
plan must pick — it cannot claim both. (My earlier "search gains a filter" intuition holds for the
variant that actually delivers the O(1) delete; the "no filter" reading is the variant that gives up the
delete win.)

### Sequencing & format constraints search forces NOW (even though it lands last)

1. **Pull the `[minKeyword,maxKeyword]` range-skip FORWARD to ship WITH S3** (metadata-only; a
   FormatVersion Open-time upgrade pass re-derives spans by scanning each segment's `[I]` band — exact
   precedent `upgradeSegmentRanges`/`reconcile.go:176-200`; **no byte reindex needed**). Otherwise S3's
   cap raises segment count with NO offsetting skip for the whole S3→S4 window. **Cap-default and
   range-skip ship-date are ONE coupled decision:** ship the skip with S3, OR keep the S3 cap default
   HIGH (inert) until S4. Range-skip gives the common-prefix shape ZERO relief, so it must NOT justify a
   lower cap.
2. **S4 skip data buys MEMORY, not single-term latency** — DECIDE whether to add a NEW store-side
   multi-term leapfrog/galloping-AND entrypoint. Without it the skip header is dead weight for latency
   (memory win only); with it, it's a new public seam that must beat the engine's smallest-set-first
   probe. This changes what S4 *is* → decide before speccing S4.
3. **Small-posting inline invariant:** chunked postings add a skip header a tiny posting must parse, and a
   prefix Search visits MANY keywords' postings → the header tax multiplies. Keep small postings on an
   inline / no-skip-header path **byte-identical to today below a docid-count crossover** — a design
   INVARIANT, not an optimization. Format must be self-describing (per-posting block size + a no-skip flag
   bit) so the crossover retunes without a second reindex.
4. **S1≠S2 core:** merge traverses oldest→newest / last-wins / adds-then-dels; search traverses
   newest→oldest / first-wins / dels-then-adds. If shared, parameterize by (direction, win-rule,
   within-source order); else two impls policed by the same byte-identical/identical-hits gates.
5. **K must be bounded by S3's cap; use an explicit k-pointer LINEAR merge, not a heap** (K small; heap is
   pure overhead on rare/exact). Honest caveat: `scanPrefix` is a PUSH callback over whole-decoded blocks,
   so for segments the streaming merge is really "buffer-then-merge" until S4 — the per-source df floor and
   thus part of the union win waits on S4.
6. **No decode/merge work back under the RLock** — snapshot is acquired ONCE; all segment I/O stays after
   `RUnlock` (`search.go:107-138`).
7. **Unify the seal-order VERSION with the segment id:** a merged output's fresh highest `segMeta.Id` IS
   the monotonic never-reused seal-order version newest-wins resolves by — fix this in S3 before P2 codes
   against version.
8. **GetDocs has NO production caller** (engine calls only `Search`) — weight GetDocs wins at ~zero; the
   dict-membership bloom-replacement may not be worth wiring (it makes a search-side path read the dict
   region + contend the `dictCache` LRU). Range-skip alone may be the whole membership story.

### Resident memory (not a search cost, but a new RSS line item)

The P2 per-table `docid→version` table is ~O(live docids) (~40–80 MiB / 10M docs), a MERGE-side structure
(NOT read by Search under the keep-dels variant). Decide its representation NOW (dense `[]version` /
two-level page table given dense monotonic docids, NOT a sparse map); D0's resident probe must account for
it so S4's "df-independent peak" claim isn't silently violated by an O(live-docs) resident table.

## 7. Updated open decisions for the maintainer (supersedes §4 where they overlap)

A. **P2 delete trade:** cheap O(1) deletes (drop per-keyword del-postings on delete) + a search-side
   liveness filter (roaring deleted-set), VS keep del-postings (search free, no delete write-win). **This
   is the decision that defines P2 — pick before speccing it.**
B. **Range-skip timing:** ship with S3 (FormatVersion upgrade pass, no reindex) **[rec]**, vs hold in S4 +
   keep cap high. Coupled with the cap default.
C. **S4 latency lever:** commit to a store-side multi-term leapfrog entrypoint (skip data → latency), or
   spec S4 honestly as a MEMORY-only win (no AND/leapfrog latency claim).
D. (carried) release shape (floor-first), cap value/chunk geometry (provisional self-describing knobs),
   forward decouple, deferred MANIFEST-O(segments)/recomputeLive-O(docs) track.
