# Renascentia core diagnoses — 2026-08-02

Written under `~/gt/GASTOWN-CORE-CHANGE-PROTOCOL.md`, which requires a written
diagnosis (symptom, root cause, proposed change, blast radius, rollback) before
a Gas Town core change lands, and a specific per-change approval from Rodrigo.

**All four** defects from the 2026-08-01 pilots are now **implemented, tested and
deployed** (`gt v1.2.1-311-g344196ed`). Defects 3 and 4 were written up here as
diagnosis-only first, deliberately: both sit on machinery whose failure modes are
documented in-tree, and both needed the root cause established before a line was
changed. Their sections below keep the diagnosis and record the fix that followed.

---

## FIXED — 1. The merge audit trail self-deletes

**Symptom.** MR bead `ba-wisp-bdb` carried pilot #1's whole review record — a
witness review, the refinery's hold reasoning, and the rulings that authorised
the merge. It was readable at 23:26 and gone by 00:05 the same night. "Why was
this merged?" became unanswerable about 39 minutes after the merge.

**Root cause.** MR beads are created `Ephemeral: true`. Ephemeral is not a TTL
flag — it is the ROUTING mechanism that puts MR beads in the `wisps` table
(`internal/beads/beads_mr.go:66`, GH#2446), and wisps are compacted away.

**Change.** `closeMRWithReason` (`internal/refinery/engineer.go`) is the single
choke point where every MR ends, merged or rejected. Before closing, it now
mirrors the MR's comments plus outcome, branch, commit and worker onto the
**source issue**, which is durable.

**Why not just make MR beads non-ephemeral:** that flips which table every MR
lives in and breaks every consumer querying
`ephemeral=true AND label=gt:merge-request` (e.g. `internal/witness/handlers.go:699`).
Mirroring is the small change.

**Blast radius.** One function plus one helper, on the close path only. The
mirror is best-effort: failures are printed, never returned, and a `recover`
contains panics — a storage backend that cannot serve comments must degrade to
"no audit mirror", never to "merge not closed". That is not hypothetical: a
partial test store made `Comments()` nil-dereference and the existing suite
caught it before commit.

**Tests.** `internal/refinery/engineer_mr_record_mirror_test.go` — mirror
content (review text, hold text, provenance, commit, branch, worker); close
still succeeds with no source issue; close still succeeds with an unresolvable
source issue. Full `internal/refinery` package green.

**Rollback.** `git revert dd2d9ff7`.

---

## FIXED — 2. Mail lists render a blank sender

**Symptom.** A merge-approval mail listed in the refinery's inbox with an EMPTY
sender. The refinery nearly held a correctly-approved MR because an instruction
to merge appeared to have no author.

**Root cause.** The `from:` label was present the whole time; the list view
rendered it blank during the in-flight window before `delivery-acked-at` was
set. `Message.Validate()` already rejects an empty `From`, so the blank was an
invariant violation being displayed as ordinary text.

**Why it matters.** The human-review gate accepts "named mail from a real town
role" as merge authorisation. A message rendering with no sender is
indistinguishable from an unattributed — or forged — instruction. Blank is the
worst possible rendering: it reads as "no sender field" rather than "this sender
did not resolve", so a reader cannot tell a display bug from missing attribution.

**Change.** `senderForDisplay()` in `internal/cmd/mail_sender_display.go`, used
by all three list views (inbox, search, channel). Prints an explicit
unattributed marker that also names the verification step (`gt show <id>`).

**Scope honesty.** This does NOT fix the underlying resolution race. The label
was always correct, so the defect being fixed is the rendering of the unresolved
case. Containing it here keeps an agent's decision correct even when resolution
lags.

**Blast radius.** Display only; no behaviour change. **Rollback.** `git revert e61d95ef`.

---

## FIXED — 3. `gt costs` omits polecats

**Symptom.** `gt costs --by-role` shows boot, deacon, mayor, witness, refinery —
and no polecat line. The role rows sum to exactly the reported total
($5,964.54 on 2026-08-01), so polecats are absent from the total, not merely
unlabelled. The actual unit of factory work is unpriced.

**Root cause — this is deliberate, not an oversight.**
`internal/doctor/claude_settings_check.go:552-555` documents it:

> - polecat → `gt tap polecat-stop-check` (idle-polecat catcher)
> - everyone else → `gt costs record` (autonomous cost accounting)

Every role has ONE Stop-hook slot. Polecats spend theirs on the idle-catcher, so
they never call `gt costs record` and never reach the ledger that `--today`,
`--week` and `--by-role` read. The live path (`runLiveCosts`) enumerates tmux
sessions, so a FINISHED polecat is invisible there too.

**FIXED in `69691492`.** Polecats now run BOTH hooks: `gt tap polecat-stop-check`
AND `gt costs record &`. This is safe precisely because of the analysis below —
the doctor's Stop check is `hookHasPattern`, a **substring** match, so
`polecat-stop-check` is still found and both sides converge instead of deleting
each other's work. Backgrounded with `&` to match the other roles, so cost
accounting never delays teardown. Tests assert both that cost recording is
present and that the idle-catcher survived alongside it — replacing rather than
adding is exactly what caused #3648.

**Why the caution was warranted.** The same comment records that this exact
area previously failed to converge: *"hooks sync wrote polecat-stop-check,
doctor demanded costs record, fix deleted the file, the daemon recreated the same
polecat-stop-check file, repeat forever"* (#3648). A change here must satisfy the
hook writer, the doctor check and the daemon simultaneously or it reopens that
loop. That is why the fix ADDS a hook rather than swapping one, and why it was
worth reading `hookHasPattern` before touching anything: had the doctor done a
deep-equality comparison instead of a substring match, this same change would
have restarted the loop.

**Known limit of the fix.** It records cost for polecat sessions that end AFTER
their settings are regenerated (`gt doctor --fix hooks-sync`). It does not
retroactively price the two 2026-08-01 pilot runs, and it does not help the live
path (`runLiveCosts`), which enumerates tmux sessions and therefore still cannot
see a polecat that has already exited. A future improvement, needing no hook
cooperation at all: have the cost walker discover polecat transcripts by path
(`~/.claude/projects/*-gt-<rig>-polecats-<name>-*`).

**Interim mitigation, already in place:** `~/.paperclip/bin/gt-polecat-cost.mjs`
prices polecat runs from their transcripts, outside Gas Town. Pilot #1 measured
152 turns / 12.9 min / ≈$11.59; pilot #2 114 turns / 11.3 min / ≈$8.11.

---

## PARTIALLY FIXED — 4. `gt nudge` has no verified sender (option B)

**Symptom.** A nudge arrives with no authenticated author. Any process on the Mac
can issue an authoritative-looking instruction to any agent, all of which run
`--dangerously-skip-permissions`.

**Status.** Contained by doctrine since 2026-08-01 (`~/gt/CLAUDE.md`,
`~/gt/AGENTS.md`): an unattributed nudge is READ-ONLY; mutating work needs a
mandate with a real author. Verified empirically — the Mayor, the Deacon and a
witness each refused mutating unattributed nudges while lifecycle work continued.
`origin:` lines are explicitly a courtesy, not proof.

**PARTIALLY FIXED in `344196ed` — and the honest half matters more than the
fixed half.** `gt nudge` no longer reports an unresolvable caller as the bare
string `"unknown"`. It reports `UnattributedNudgeSender`, a self-describing
marker stating that attribution failed and that the nudge is read-only. Doctrine
keys on that string, and tests pin it so a silent rename cannot quietly disarm
the rule. Doctrine updated the same day (`~/gt` commit b5ed7af).

**What is NOT fixed, stated plainly: this is naming, not verification.** Under
`--mode=immediate` a nudge is delivered as tmux send-keys TEXT. A receiver cannot
verify ANY sender claim — including a true one — because the prefix is just
characters in a pane. Structured attribution exists only in queue mode, where
`nudge.Enqueue` stores `Sender` as a field in the town's own store rather than as
pane text.

**Real option B, still open.** Route mutating nudges through the queue path and
have receivers read `Sender` from the store instead of from the message body.
That is a transport change and deserves its own design; the sequencing reason for
deferring it (don't patch `done.go`'s neighbourhood twice) is now gone.

**One finding that should shape it.** Doctrine currently tells agents to weigh
`origin:` lines as untrusted text. If nudges gain real attribution, that rule
must change in the same commit, or agents will keep discounting attribution that
has become trustworthy.

---

## Rebase provenance

`rebase/upstream-2026-08` = upstream `649b832b` + 2 commits.

The seven pre-rebase commits were reduced to two **without dropping any
content** — the tree is byte-identical to the old `renascentia/custom`. The six
"checkpoint-dog auto-save" commits shared one generic message but contained six
DIFFERENT changes totalling ~2,700 lines: the launch gate (`launch_gate.go`,
+565 and its test +241), GBrain worker launch (+316/+339), convoy bd routing,
formula and sling-dispatch-readiness work, and escalation work. Treating them as
noise — as their identical messages invite — would have destroyed all of it.

**Dropped deliberately, one item:** our convoy bd-routing patch. Upstream's
`ShowMultiple` now groups IDs by `ResolveRoutingTarget` and pins a client per
directory — the same fix, implemented more thoroughly. Verified by reading the
upstream implementation, not assumed.

**The double-submit fix is still required.** Upstream's `ListMergeRequests`
still does `if sqlErr == nil && ...`, silently ignoring a failed wisps query and
returning a partial list. Proven with the arbiter test, which FAILS on pristine
upstream (`expected an error when the wisps query fails, got nil`) and PASSES on
ours, before and after the rebase.

**Test result.** 10 new failures appeared post-rebase; all 10 fail identically on
pristine upstream `649b832b`, verified in a separate control clone. The rebase
introduced **zero** new failures and fixed 15 that were failing before it.
