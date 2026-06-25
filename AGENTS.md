# AGENTS.md — Working Principles for AI Agents

Mandatory working principles for any AI agent (Claude Code, etc.) operating in this
repository. They override default behavior. **Read them at the start of every session.**

## 0. NEVER write code directly — the SDD flow is mandatory, no exceptions

For any code change, however small or "obvious", you MUST follow this pipeline IN ORDER and
NEVER skip a stage:

1. **Spec** — write the design/spec first (chunked per Principle 3).
2. **Multi-agent review** — dispatch MULTIPLE independent review agents to cross-review the
   spec; fix every blocker/major before proceeding.
3. **Task breakdown** — decompose into bite-sized TDD tasks.
4. **Multi-agent cross-review** — multiple agents cross-review the task breakdown; fix issues.
5. **Implementation — driven by a WORKFLOW, never by hand.** TDD (red → green), **ONE item at a
   time**, orchestrated through the Workflow tool (multi-agent): the coordinator MUST NOT hand-edit
   product code in the main loop — every code edit happens inside a workflow subagent. For each item
   the workflow: writes the failing test → runs it red → implements → runs it green → runs the gates,
   then dispatches MULTIPLE independent review agents and LOOPS (fix → re-review) until that item
   returns zero blocker/major; only THEN commits it and moves to the next item. Never batch several
   items before reviewing. If you catch yourself opening an editor on product code outside a workflow,
   STOP — author the workflow instead.

**The review stages are a LOOP, not a single pass — re-review until clean.** Whenever you fix
findings from a review (stage 2, 4, or 5), you MUST dispatch a FRESH round of multiple independent
review agents on the REVISED artifact and repeat — your own edits are unverified until a new review
round confirms them, and a fix routinely introduces a new blocker (e.g. a deadlock fix that
reintroduces the deadlock elsewhere). Keep iterating rounds until a full round returns **zero
Blocking and zero Major** findings. Do NOT advance to the next stage, and do NOT report the artifact
as done, after merely *applying* fixes — applied-but-not-re-reviewed is not done. Record each round's
findings + resolutions in the artifact so the convergence is auditable.

Do NOT jump straight to editing code, not even for a "quick prototype", a "let me just
measure it" spike, or a one-line fix. Prototyping a change before the spec/review is still
"writing code directly" and is forbidden. Measurement that requires new/changed product code
follows the same flow. If you catch yourself opening an editor before the spec is written and
reviewed, STOP and go back to stage 1.

## 1. Infrastructure: ship any real benefit, however small

This work is infrastructure. If a change produces a **real, correct benefit — even a
tiny one — do it.** Do not skip a sound improvement because the measured win looks
marginal, and do not abandon the straightforward improvement in favor of a "bigger
lever" or an easier alternative.

- Don't reject a fix as "not worth it" on size alone — a small, real reduction in work
  (fewer fsyncs, fewer allocations, less I/O, less churn) is worth making on its own.
- Don't substitute a different, larger-scope change for the obvious small one.
- Do the complete job: sweep **all** the safe cases, not just the big ones.

## 2. Verify at the source — no substitute environment or method

**Verify a problem, and its fix, WHERE the problem actually occurs.** Do not use a
proxy environment or a substitute method and then draw conclusions from it.

- If the problem is on CI (a specific OS/runner), reproduce and measure **on that CI**.
  A local machine is NOT a valid stand-in — e.g. a local disk/AV setup behaves nothing
  like a hosted Windows runner, so a local measurement of an AV/fsync-bound effect
  proves nothing about CI.
- Don't be clever or presumptuous. Go to where the issue is, reproduce it there, and
  validate the fix there.
- Do not propose unrequested "alternative approaches" in place of verifying the real
  thing at its source. Don't spin up a new worktree/PR for a small in-flight change —
  make it directly in the branch you are already working in.

When the two meet: make the real infrastructure improvement (Principle 1) **and** prove
it in the real failing environment (Principle 2) — never in a convenient substitute.

## 3. Author large files incrementally — chunk, don't dump

When creating a large file (a plan, spec, design doc, or sizable code file), **do not
emit the whole thing in one giant write.** Build it up in chunks: create the file with
its header/skeleton first, then append one section at a time.

- Write the file in pieces (header → section → section …), each a small, self-contained
  append — not a single multi-hundred-line dump.
- This keeps every step reviewable, lets the user course-correct mid-way instead of
  after the entire artifact lands, and produces a cleaner edit history.
- Applies to generated docs and plans especially, but to any long file: prefer a
  sequence of focused appends over one monolithic write.

## 4. A perf demo must be format-identical to the real implementation

When you measure a design with a demo/prototype/spike, **the demo's on-disk format and
data path must be EXACTLY what the real code will implement.** No simplified, packed,
"good-enough", or approximate version is acceptable as a source of numbers.

- **The disk format is the contract.** The byte layout the demo writes MUST be the byte
  layout the production code writes — same blocks, same indexes, same chunking, same
  encodings. A simplified layout produces simplified (i.e. wrong, usually optimistic)
  numbers — disk size, memory, read amplification all change with the format.
- **Every feature is measured through the demo, not estimated.** If a feature exists in
  the design (the forward map, tombstones, compression, merge, large-value chunking),
  it must be present and exercised in the demo before any number that involves it is
  reported. "Inverted-only", "merge handled separately", "forward estimated" etc. are
  self-deception — the deployed system always pays those costs, so the measurement must
  too.
- The CODE may be rough (messy, unfactored, demo-quality) — that is fine. The FORMAT and
  the set of features exercised may **not** be rough or partial.
- If a measurement was taken on a simplified path, it does not count. Rebuild the demo to
  the real format and re-measure.

This is the data-integrity counterpart to Principle 2: Principle 2 says measure in the
real *environment*; Principle 3 says measure with the real *format and feature set*.
