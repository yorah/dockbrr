# SDD workflow: dockbrr process rules

How subagent-driven development (superpowers:subagent-driven-development) runs in this repo.
These rules were learned the hard way in Phases 5–7; start every new phase already following them.

## Session hygiene (token discipline)

- **Fresh session per phase/batch.** Controller starts with exactly three inputs: the plan path, the ledger path, and CLAUDE.md (auto-loaded). Nothing else carried over.
- **Never paste the plan into context.** Phase plans run 1,500–4,800 lines. Dispatch from the plan's task summary table (see "Plan format"); `scripts/task-brief` (superpowers plugin) extracts the per-task section into a brief file.
- **Handoff between sessions via the ledger**, not conversation. If mid-phase context runs out, use /handoff and resume from the ledger.

## Scratch layout: namespace per phase

```
.superpowers/sdd/phase-N/
  progress.md            # ledger for this phase only
  task-M-brief.md
  task-M-report.md
  review-<a>..<b>.diff
```

Phase-7 gotcha: `scripts/task-brief` default output collided with earlier phases' briefs and subagents read stale ones. Per-phase directories kill this class of bug. Regenerate every brief from the current plan before dispatching; verify the brief heading matches the task.

## Dispatch contract (what each subagent receives, and nothing more)

- **Implementer**: brief file path + absolute worktree path + the 7 safety invariants (CLAUDE.md). Not the plan, not the conversation. Writes `task-M-report.md`.
- **Reviewer**: review diff file path + brief file path + invariants. Diffs pre-generated to a file (`git diff a..b > review-a..b.diff`); reviewer never regenerates from conversation state.
- **Controller** reads the report file, not the subagent transcript.
- **ALWAYS absolute paths** in dispatch prompts (e.g. `/home/yorah/projects/dockbrr/.claude/worktrees/<branch>/.superpowers/sdd/phase-N/...`). Subagents that don't `cd` resolve relative paths against the MAIN checkout and read stale files (bit us in Phase 7, Task ≤12).

## Model tiering (worked well in Phase 7)

- **cheap/haiku**: scaffolding, mechanical edits, config plumbing.
- **sonnet**: default for implementation tasks and routine reviews.
- **opus**: security-sensitive tasks (auth, CSRF, sanitization, embed/serve, SSE) and their reviews; always the whole-branch final review.

Record the per-task model assignment in the ledger before starting.

## Ledger (progress.md) rules

- One entry per task: commit range, review verdict, deviations, Minors (numbered `[TN-MK]`).
- Record resolved design forks (AskUserQuestion outcomes) at the top, durable ones also go to `docs/dev/specs/`.
- Triaged Minors carry to the whole-branch review; none silently dropped.
- **The ledger is gitignored scratch. It dies with the worktree.** Any Minor
  the final review *defers* (not fixed in-branch) MUST be flushed to
  `docs/dev/backlog.md` (tracked) and committed BEFORE the worktree is
  removed. Do not `git worktree remove` while the ledger names an unresolved
  deferred item that isn't yet in the backlog, that is how they get forgotten.

## Failure protocols (both exercised in Phase 7, both worked)

- **Subagent dies mid-task** (529 / session limit): controller verifies tests + commits the completed work itself, then dispatches the reviewer on the committed diff. If the agent is resumable, SendMessage-resume retains its context.
- **Reviewer says Needs-fixes**: controller applies the reviewer's prescribed fix, verifies, amends the ledger entry (`Needs-fixes→fixed`).

## Plan format

Every phase plan starts with a task summary table so the controller can dispatch without reading the body:

```markdown
| # | Task | Deps | Model | Reviewer | Plan section |
|---|------|------|-------|----------|--------------|
| 1 | password endpoint |, | sonnet | opus | L120–L340 |
```

Plans live in `docs/dev/plans/`; move to `plans/archive/` when the phase merges.

## Known gotchas

- **Apply health-gate**: must poll the RECREATED container ids after `up`; polling pre-apply ids records real applies as failed (Phase 6).
- **Task reorder is fine** when dependencies demand it (P7 ran 10 before 9): note it in the ledger.
- **Briefs can contain wrong snippets**; implementers may deviate when the codebase contradicts the brief, but the report must call the deviation out and the reviewer must judge it.
