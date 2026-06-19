# AGENTS.md — Working Principles for AI Agents

Mandatory working principles for any AI agent (Claude Code, etc.) operating in this
repository. They override default behavior. **Read them at the start of every session.**

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
  thing at its source.

When the two meet: make the real infrastructure improvement (Principle 1) **and** prove
it in the real failing environment (Principle 2) — never in a convenient substitute.
