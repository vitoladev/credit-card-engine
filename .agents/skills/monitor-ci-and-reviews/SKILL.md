---
name: monitor-ci-and-reviews
description: |
  Watch a pull request's checks and incoming review until they settle, then
  triage what came back. Use after pushing to a PR branch, or when asked to
  wait for CI or for a PR to go green.
---

# Monitor CI and reviews

Waiting is the work: a check that flips red twenty minutes from now matters as
much as one that is red already. Arm a watch, keep working, and triage what
lands. `gh` runs on the host.

## 1. Resolve the branch and its base

```bash
git branch --show-current
gh stack view --json 2>/dev/null    # empty/error when this is not a stack
```

The current branch owns one PR. In a stack, a layer is only green when every
layer **below** it is green too — a red base makes this layer's pass
meaningless, so watch both. Read the whole stack's PR numbers out once, here.

## 1b. Know whose review to wait for

Read the repo's `review_bot` binding from `.agents/orchestrator.json`,
or from the repo's `CLAUDE.md` when it has no overlay. The
`task-orchestrator` skill's `scripts/bindings.sh review_bot` prints the
merged value. When the binding is null, CI is the only gate: arm §3 and
skip §3b. Otherwise it names `author` (the login the bot's reviews and
threads carry), `check` (a status check that says the reviewer ran),
`verdict` (a check that must pass on the current head, or null), and
`skip_on` (the layer classes the bot never reviews).

For each PR, classify the head against its base:

```bash
base=$(gh pr view <n> --json baseRefName -q .baseRefName)
git diff --name-only "origin/$base"...$(gh pr view <n> --json headRefOid -q .headRefOid)
```

A layer is `docs-only` when every changed path ends in `.md`, else
`code`. A layer whose class is in `skip_on` gets §3 only. It is
merge-ready when the required checks are green, and a missing `check` or
`verdict` is expected, not pending. Every other layer gets §3 and §3b,
and merge-ready also needs the review-clean rule.

A layer is review-clean under one rule. When `verdict` is set, that check
passes on the current head. When `verdict` is null, the latest review by
`author` is on the current head and no thread that `author` opened is
unresolved.

## 2. Know what checks to expect

**Discover the checks; do not assume one.** Workflow files are not the whole
list — GitHub Apps (review bots, coverage, preview deploys) post checks
independently of any workflow in the repo, so a check can arrive that no YAML
in the tree declares. Read what actually reports:

```bash
gh pr checks <n> --json name,bucket,workflow
ls .github/workflows/
```

Build the expected set from a previous run on the same base rather than from
the YAML alone. **Do not conclude from a workflow's triggers that nothing will
come** — an app-posted check has no trigger you can read.

A red check from a review bot is usually *not* a build failure. Read its
summary: "outstanding review feedback" means go read the review (step 5), not
hunt for a failing job.

**The `paths-ignore` trap.** A workflow carrying `paths-ignore` (commonly
`['**.md']`) means a diff touching only those paths triggers **no run at all**.
That is not "still pending"; it is "nothing will ever report". Check what the
diff touches before concluding a PR is stuck:

```bash
[ "$(git diff --name-only <base>...HEAD | grep -cv '\.md$')" -gt 0 ] \
  && echo "CI will run" || echo "markdown-only: no CI run expected"
```

**Do not write this as `grep -qv`.** Where `grep` is ugrep, `-q` returns the
wrong exit status combined with `-v`: it reported "markdown-only" for a diff
containing twenty non-markdown files while CI was demonstrably running.
`grep -cv` and a numeric comparison are reliable everywhere.

## 3. Arm a watch per PR

One watch per PR, emitting each check as it reaches a terminal state and
exiting when all of them have:

```bash
prev=""
for i in $(seq 1 80); do
  s=$(gh pr checks <n> --json name,bucket 2>/dev/null || echo '[]')
  cur=$(jq -r '.[] | select(.bucket!="pending") | "PR<n> \(.name): \(.bucket)"' <<<"$s" | sort)
  comm -13 <(echo "$prev") <(echo "$cur")
  prev=$cur
  jq -e 'length>0 and all(.bucket!="pending")' <<<"$s" >/dev/null && { echo "PR<n> SETTLED"; break; }
  sleep 30
done
```

`bucket` is `pass` / `fail` / `skipping` / `cancel` / `pending`, so selecting
everything that is not `pending` reports failures and cancellations as loudly
as passes. A filter matching only `pass` goes silent on a red run, and silence
is indistinguishable from still-running.

`length>0` guards the first seconds after a push, when the checks array is
empty and `all()` is vacuously true — without it the watch exits immediately
and reports green before a single job has started. Note this same guard is
what makes the no-run case in step 2 spin to the full 80 iterations rather
than exiting: recognise it from the diff, not from the watch.

Poll at 30s or slower; `gh` shares the GitHub API rate limit with everything
else in the session. Then keep working — the notification arrives on its own.

## 3b. Arm a review watch per reviewed PR

Skip the layers §1b excluded. The bot's `check` going green only means
the reviewer ran. The verdict is the `verdict` check, or the thread rule
when `verdict` is null. One `Monitor` per PR turns each new review by
`author` and each new unresolved thread it opened into an event, and
exits once the reviewer has spoken on the current head:

```bash
n=<pr>; author=<review_bot.author>; head=$(gh pr view $n --json headRefOid --jq .headRefOid)
seen=""
for i in $(seq 1 60); do
  j=$(gh api graphql -f query='query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){reviews(last:20){nodes{author{login} submittedAt commit{oid} body}} reviewThreads(first:100){nodes{id isResolved path line comments(first:1){nodes{author{login} body}}}}}}}' -f o=<owner> -f r=<repo> -F n=$n)
  cur=$(jq -r --arg h "$head" --arg a "$author" '
    (.data.repository.pullRequest.reviews.nodes[] | select(.author.login==$a and .commit.oid==$h) | "PR'"$n"' review @\(.commit.oid[0:7]): \(.body | split("\n")[0] | .[0:100])"),
    (.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved|not) | select(.comments.nodes[0].author.login==$a) | "PR'"$n"' thread \(.id) \(.path):\(.line // "-") \(.comments.nodes[0].body | split("\n")[0] | .[0:100])")' <<<"$j" | sort)
  comm -13 <(echo "$seen") <(echo "$cur"); seen=$cur
  jq -e --arg h "$head" --arg a "$author" '[.data.repository.pullRequest.reviews.nodes[] | select(.author.login==$a and .commit.oid==$h)] | length>0' <<<"$j" >/dev/null && { echo "PR$n REVIEWED @${head:0:7}"; exit 0; }
  sleep 45
done
echo "PR$n REVIEW WATCH TIMEOUT — no review by $author on ${head:0:7} after 45 minutes"
```

The watch ends with a line either way, `REVIEWED` or `REVIEW WATCH
TIMEOUT`, because a loop that only reports success is silent on exactly
the failure it exists to catch. The watch matches the review on
`commit.oid == head`, never on `submittedAt` or on a check's bucket. A
review posted seconds after a push is usually on the previous head, and
a "ran" check passes even when the review requests changes. The watch
filters threads to unresolved ones opened by `author`, so your own
replies and resolutions do not re-fire.

A rebase-only push may earn no new review. A bot that keys on content
treats a head whose diff has the same `git patch-id --stable` as the head
it reviewed as already reviewed. Arm the watch on the reviewed head rather
than waiting for a review that never comes. In a stack that means one
review per layer per iteration, not one per rebase.

Then loop **one pass per iteration**: read every layer's findings before
fixing any, fix them bottom-up, and push the whole stack once. A push
after each layer rebases every layer above it and the bot re-reviews each
moved head. Fix what is real, reply-and-resolve what is not
(`/resolve-pr-comment`), then re-arm both watches on the new heads. Check
a finding that names a compile error, a missing symbol, or a wrong
identifier against the CI job and a `grep` before it earns a commit.
Review bots read diffs, not build output. The loop ends when
every reviewed layer is review-clean (§1b) on its current head.

## 4. Read the state, not the label

```bash
gh pr view <n> --json mergeable,mergeStateStatus,reviewDecision,statusCheckRollup
```

`mergeStateStatus` is `CLEAN` when it is ready, `UNSTABLE` while checks run,
`BEHIND` when the base moved, and `DIRTY` for conflicts. **It is computed
lazily**: straight after a push it can still describe the previous head, and a
`DIRTY` that contradicts a clean local

```bash
git merge-tree --write-tree <base> HEAD
```

is stale rather than true. This one costs real time — a PR can report
`CONFLICTING` for a base branch that is already an ancestor. Re-read it before
believing it.

`reviewDecision` **survives a force-push**, so an `APPROVED` may predate the
commits under review. Trust the review whose `submittedAt` is later than the
current head SHA, or a bot's own check run — not the summary field.

## 5. Triage what settled

**A red check.** Pull the failing step rather than guessing from the name:

```bash
gh run view <run-id> --log-failed
```

Reproduce it locally through the repo's own gates before editing, routing the
commands through the repo's command boundary when it has one. Commit the fix
on the branch that owns the code, which for a stack is often below the layer
that went red.

**A stale body.** Fixes pushed while waiting make the description lie. Repair
it with `/maintain-pr-description`, and re-check that Preview media still
matches what the branch now does — a recording of superseded behaviour is
worse than none.

Re-arm the watch after every push. A fix is not green until its own run says
so, and reporting the previous run's pass as the current state is the one
failure this skill exists to prevent.

## 6. Report

Give the per-check result for each layer, the merge state, whether any review
is current with the head SHA, and what is left to do. Name anything still
pending as pending rather than rounding it to green — and name a PR whose diff
triggers no run as "no CI expected" rather than as either.

Done when every check on this branch and every layer below it has reached a
terminal state (or is correctly identified as one that will never run), each
red one is fixed or explained, and the reported state matches what
`gh pr checks` prints right now.
