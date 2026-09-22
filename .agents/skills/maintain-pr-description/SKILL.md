---
name: maintain-pr-description
description: |
  Rewrite a pull request body so a reviewer reading only the summary sees
  HEAD, not the first submit. Use after landing commits or review-driven
  fixes on a branch whose PR is already open.
---

# Maintain PR description

The body is what a reviewer reads. It is written once at submit; every later
commit on the branch makes it stale. Repair it in place. `gh` runs on the
host, through `gh`. The body is what you edit; a comment is a different
artifact and does not repair it.

## 1. This branch's PR

Stacked work: the current branch owns one PR. Edit the PR of the branch that
is checked out.

```bash
gh stack view --json 2>/dev/null   # empty/error when this is not a stack
git branch --show-current
gh pr view --json number,url,title,body,baseRefName
```

The diff that belongs in the body is this PR against **its base**, not the
default branch (that would swallow every layer below):

```bash
git log --oneline <base>...HEAD
git diff --stat <base>...HEAD
```

`<base>` is the branch below in `gh stack view --json` (the default branch for
the bottom layer, or for an unstacked PR). Confirm it matches `baseRefName`.

## 2. What to keep, what to rewrite

Read the current body first. Preserve:

- Issue (and parent) links
- A `## Preview` section if present (media URLs, GIFs, stills, the media-branch
  note) — `/pr-preview-media` owns that block
- Checked acceptance criteria **and their evidence quotes**, unless the
  criterion itself no longer applies
- The existing generated-by footer, if any
- A stacking note naming the layer below

Rewrite so the rest matches HEAD, in the repo's own vocabulary. Every number,
verdict and command output traces to a run you performed.

If `.github/PULL_REQUEST_TEMPLATE.md` exists, keep its headings. Otherwise
keep the body's current headings. If the body is still auto-generated text (no
`## Summary`), use:

```
## Summary
## Changes
## Acceptance criteria
## Tests
## Review
## Known gaps
```

`## Preview`, when present, stays where `pr-preview-media` put it: after the
lead section, before `## Changes`. Never drop it in a rewrite. When the repo's
template makes preview media required for a surface this diff touches, a
missing `## Preview` is a gap to report, not a section to quietly omit. Its
media URLs, before/after pair and run link are facts you did not gather: carry
them across verbatim.

## 3. Fill the sections

**Summary.** One paragraph: which issue, what this slice does *now*. Not a
changelog of commits.

**Changes.** The slice as of HEAD vs base, grouped by concern, not a file list
and not only the latest commit. Drop bullets the diff no longer contains; add
ones it now does (tests, docs, skills).

**Acceptance criteria.** Leave `[x]` as `[x]` unless a later verifier run
failed it. The criteria are the ones the issue and contract require. A
Blocked criterion stays visible.

**Tests.** Only numbers and commands you observed, or keep the last recorded
run and note it is from an earlier SHA.

**Review.** Report the last code-review verdict and its SHA, as it stands.
Remove findings the current code no longer has; leave standing ones (and say
they still stand).

**Known gaps.** Delete lies ("no CI yet" when workflows exist). Keep real gaps.

**Title.** `gh pr edit <n> --title` only when the current title no longer names
the slice. Keep the repo's existing title convention.

## 4. Publish

Write the full body to a temp file and replace, never patch via a comment:

```bash
gh pr edit <n> --body-file <tmp>
```

Spot-check with `gh pr view <n> --json body`. Done when a reviewer who opens
only the summary would describe the same slice `git diff <base>...HEAD` shows,
and Preview / issue links / evidence quotes are still there.
