---
name: create-pr
description: |
  Open this branch's pull request: body from the repo template, preview media
  uploaded into it with gh --attach. Use when a branch has commits but no PR,
  or after gh stack adds a layer.
---

# Create PR

Opens a PR that does not exist yet. `gh` and `git` run **on the host**;
nothing here goes through the repo's command boundary except the test
evidence in step 3.

Media goes **in the body** via `gh pr edit --attach`, which uploads a local
file to GitHub's own asset host and rewrites the path where the body already
references it. That replaces the `pr-<n>-media` orphan branch entirely — no
media branch, nothing to clean up when the PR closes. Step 4 gates on the `gh`
version and names the fallback.

## 1. Confirm the branch needs a PR

```bash
git branch --show-current
gh stack view --json 2>/dev/null    # empty/error when this is not a stack
gh pr view --json number,url,state 2>/dev/null
```

The current branch owns one PR. A `number` here means the PR already exists —
edit that body rather than opening a second one (`/maintain-pr-description`
owns that). Push first if the branch has unpushed commits; a PR opened on a
stale remote shows the wrong diff.

Never open a PR from the default branch. If `git branch --show-current` names
it, branch first.

For a stack, the base is the branch **below** this one in `gh stack view`,
never the default branch for an upper layer — that would swallow every layer
beneath it.

## 2. Open it

Stacked work goes through the `gh-stack` skill, opening one PR per layer
against the right base:

```bash
gh stack submit --auto
```

`--auto` is required — bare `submit` prompts per PR and blocks. `submit`
writes placeholder text, so the body is still step 3's job.

Off a stack:

```bash
gh pr create --base <default-branch> --title <t> --body-file <tmp>
```

## 3. Write the body

Start from `.github/PULL_REQUEST_TEMPLATE.md` when it exists and keep its
headings. Fill from the diff of this branch against **its** base:

```bash
git log --oneline <base>...HEAD
git diff --stat <base>...HEAD
```

Carry across, because a reviewer reads only this: the issue link, the stacking
note naming each layer and the reading order when there is a stack, and
evidence you actually observed — commands you ran with their real output, and
acceptance criteria each paired with the response that proves it. Run the
repo's checks through its command boundary, since that is often the only place
the toolchain resolves.

State a criterion the branch does not meet as blocked, and say what blocks it.
An unrun verifier, an invented coverage number, or a code-review verdict you
did not get costs the reviewer more than an admitted gap.

## 4. Fill the Preview section

Every PR touching a user-visible surface ships recorded proof of what it
changed: per surface, an MP4 when the proof is an interaction and a still when
a single frame carries it. A reviewer reads the summary top to bottom and
never scrolls into comments, so this is the only place they see the work run.

The media cannot exist when the PR is first opened — the recordings come from
a run of the branch — so open the PR with the section marked pending and
return here once you have files. `verify-frontend-output` drives the stack;
`pr-preview-media` records and converts what it leaves behind. Both run
locally: `--attach` uploads from disk, so nothing waits on CI, and a passing
verification is what makes the recording evidence.

### A changed surface carries before **and** after

Greenfield is the narrow case: a screen, component or flow this PR
introduces, which carries the after alone and says
`New surface — no before state`.

Everything a user could already reach is a changed surface and needs both
states. **Judge it per surface, not per PR:** a PR that adds a new screen and
also drops one control into an existing sheet has one greenfield surface and
one changed surface, and the changed one still owes a before. A field added to
a form, a new row action, a reworded empty state — each is a changed surface.
The point of the pair is showing what a user loses as well as what they gain,
which a single after shot cannot do.

`before` comes from the base branch — run the same scenario against a checkout
of it. Cropping or re-staging the after shot is not a before.
`pr-preview-media` has the mechanics: naming and matching widths.

### Attaching

`gh pr edit --attach` uploads each file and rewrites the local path the body
references. Its version gate, alt-text rules, format limits and the
authenticated check are in
[`../pr-preview-media/references/gh-attach.md`](../pr-preview-media/references/gh-attach.md)
— read it before the first attach.

```bash
gh pr edit <n> --body-file <tmp> \
  --attach './checkout.mp4' \
  --attach './events-list.png#Events list rendering three published events'
```

A PR with no user-visible surface — backend, infra, docs — replaces the whole
section with `Not applicable — no user-visible surface changes.` A diff that
touches the frontend and claims that line is wrong: find the surface it
changed.

## 5. Verify what a reviewer will see

```bash
gh pr view <n> --json number,url,title,baseRefName,body
```

Then run the authenticated asset check from
[`../pr-preview-media/references/gh-attach.md`](../pr-preview-media/references/gh-attach.md).
An attach that partly failed still rewrites the body, so the markdown looking
right proves nothing.

## 6. Ask for review, then watch

Opening the PR is not the end of the layer. Read the repo's review-bot
binding: `review_bot` in `.agents/orchestrator.json`, or the repo's
`CLAUDE.md` when it has no overlay. The `task-orchestrator` skill's
`scripts/bindings.sh review_bot` prints the merged value. Then classify
the layer:

```bash
base=$(gh pr view --json baseRefName -q .baseRefName)
git diff --name-only "origin/$base"...HEAD
```

A layer is docs-only when every changed path ends in `.md`. Otherwise it
is code. When `review_bot` is null, post nothing. When its `trigger` is
set and the layer's class is not in its `skip_on` list, post the trigger
once:

```bash
gh pr comment <n> --body '<review_bot.trigger body>'
```

A null `trigger` means the bot reviews every PR on its own. On a
docs-only layer, run the repo's writing gates instead (`/technical-writing`
then `/unslop`, when it has them) and commit the prose fixes on the
branch.

Then run `/monitor-ci-and-reviews` on this PR and on every layer below it
in a stack. That skill owns the watches, the triage loop, and when to call
`/resolve-pr-comment`. An open PR is not a green PR. A layer is
merge-ready only when the required checks pass on the current head and,
for a layer the bot reviews, the layer is review-clean. When `verdict` is
set, review-clean means that check passes on the current head. When
`verdict` is null, it means the latest review by `author` is on the
current head and no thread that `author` opened is unresolved.

Done when the PR is open against the right base; every surface the diff
touches appears in Preview, each changed one with both states and each
greenfield one saying so; every asset answers 200 as an authenticated viewer;
a reviewer reading only the body would describe the same slice
`git diff <base>...HEAD` shows; and the review trigger, when the binding
has one, is posted.
