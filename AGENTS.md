# AGENTS.md — Working Principles for AI Agents

Mandatory working principles for any AI agent (Claude Code, etc.) operating in this
repository. They override default behavior. **Read them at the start of every session.**

## 0. Every change happens in a git worktree — no exceptions

**Before making ANY change to this repository — code, tests, docs, config, even a
one-line edit or an edit to this file — work inside a dedicated git worktree, never
the primary `main` checkout.** There are no exceptions and no "too small to bother"
cases.

- At the start of any task that will modify files, create/enter a worktree FIRST
  (native `EnterWorktree`, else `git worktree add` under `.claude/worktrees/`).
- Never edit files in the primary working tree. If you have already started there,
  move the changes into a worktree (e.g. `git diff > /tmp/p.patch`, apply it in the
  worktree) and `git restore` the main checkout to clean before continuing.
- This isolates in-flight work, keeps `main` pristine, and makes every change
  reviewable as its own branch.

## 1. Code changes follow spec → review → task breakdown → review → implementation — no exceptions

**Never jump straight to editing code.** Every change to code (and the tests/config that
accompany it) goes through this flow, in order. Each step produces a **written artifact**,
and each **review** gate must be explicitly approved before the next step starts:

1. **Spec** — write WHAT changes and WHY: problem, goals / non-goals, design, the
   interfaces & files affected, durability / compatibility impact, risks, and how it
   will be verified.
2. **Review** — the spec is reviewed and approved before any decomposition.
3. **Task breakdown** — decompose the approved spec into concrete, ordered,
   independently-verifiable tasks.
4. **Review** — the task breakdown is reviewed and approved.
5. **Implementation (SDD)** — implement strictly per the approved spec and tasks. If
   reality diverges from the spec, STOP and amend the spec (back through review) — do
   not improvise in code.

A plan sketched in chat is NOT a spec. Measurement / exploration spikes are allowed
*before* the spec (to inform it), but production code changes wait for an approved spec
**and** task breakdown. There is no "too small to spec" exception.

## 2. Infrastructure: ship any real benefit, however small

This work is infrastructure. If a change produces a **real, correct benefit — even a
tiny one — do it.** Do not skip a sound improvement because the measured win looks
marginal, and do not abandon the straightforward improvement in favor of a "bigger
lever" or an easier alternative.

- Don't reject a fix as "not worth it" on size alone — a small, real reduction in work
  (fewer fsyncs, fewer allocations, less I/O, less churn) is worth making on its own.
- Don't substitute a different, larger-scope change for the obvious small one.
- Do the complete job: sweep **all** the safe cases, not just the big ones.

## 3. Verify at the source — no substitute environment or method

**Verify a problem, and its fix, WHERE the problem actually occurs.** Do not use a
proxy environment or a substitute method and then draw conclusions from it.

- If the problem is on CI (a specific OS/runner), reproduce and measure **on that CI**.
  A local machine is NOT a valid stand-in — e.g. a local disk/AV setup behaves nothing
  like a hosted Windows runner, so a local measurement of an AV/fsync-bound effect
  proves nothing about CI.
- Don't be clever or presumptuous. Go to where the issue is, reproduce it there, and
  validate the fix there.
- Do not propose unrequested "alternative approaches" in place of verifying the real
  thing at its source. Don't fragment a small in-flight change into a separate,
  deferred branch/PR to avoid doing it now — make it part of the work you are already
  doing, in your current worktree (Principle 0).

When the two meet: make the real infrastructure improvement (Principle 2) **and** prove
it in the real failing environment (Principle 3) — never in a convenient substitute.

## 4. Author large files incrementally — chunk, don't dump

When creating a large file (a plan, spec, design doc, or sizable code file), **do not
emit the whole thing in one giant write.** Build it up in chunks: create the file with
its header/skeleton first, then append one section at a time.

- Write the file in pieces (header → section → section …), each a small, self-contained
  append — not a single multi-hundred-line dump.
- This keeps every step reviewable, lets the user course-correct mid-way instead of
  after the entire artifact lands, and produces a cleaner edit history.
- Applies to generated docs and plans especially, but to any long file: prefer a
  sequence of focused appends over one monolithic write.
