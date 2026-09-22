---
name: pr-preview-media
description: |
  Turn a passing frontend verification into the MP4s and stills a PR's
  Preview section needs, and upload them into the body with gh --attach. Use
  once a frontend verification passes on this branch.
---

# PR preview media

A reviewer reads the PR summary top to bottom and does not scroll into
comments — the recordings belong in the body. `gh` uploads them there
directly; this skill is what gives it something true to upload. None of it
touches application code.

The source is a **passing verification run against a real stack**. Its
assertions are what make a recording evidence rather than decoration: a video
proves nothing on its own, and a screenshot of a screen nobody drove proves
less. Drive and assert first. If the run failed, there is no Preview to
publish — only a finding to report.

## Project bindings

- **Command boundary** — recording drives the browser, so it runs wherever the
  browser driver lives (commonly inside the repo's container). `ffmpeg`, `git`
  and `gh` run on the host. When the workspace is bind-mounted, a recording
  written under the repo from inside is already on the host for conversion;
  anything written to a container-only path is not.
- **Recording source** — the verification run's artifacts, or a scripted
  scenario driving the same flow.

## 1. Convert — match the medium to the claim

Pick the form from what the scenario actually proves, one per surface:

- **MP4** when the proof is an interaction and the order matters — a selection
  that routes somewhere, a signed-out action bouncing to login, a row leaving a
  tab after approval.
- **PNG** when a single frame carries it — an empty state, a rendered list, a
  form showing which options it offers, an error banner. **Reach for this
  first.** A reviewer reads a still instantly, and a video of a static screen
  spends their attention for nothing.

A before/after pair is two stills, whatever the surface does.

```bash
# Interaction -> MP4. yuv420p and +faststart make it play inline on GitHub;
# libx264 refuses odd dimensions, so round both down to even.
ffmpeg -y -i <in>.webm -an -c:v libx264 -pix_fmt yuv420p -movflags +faststart \
  -vf "scale=trunc(iw/2)*2:trunc(ih/2)*2" <out>.mp4

# State -> a late frame. `-sseof` is an input option, so it needs its own run.
ffmpeg -y -sseof -0.3 -i <in>.webm -frames:v 1 -vf scale=800:-2 <out>.png
```

A `page.screenshot()` taken inline by the scenario is a better still than a
seeked frame: it is the moment the assertion passed, not whatever the
recording ended on.

**Read every png before publishing.** A blank frame means the seek landed
outside the recording — and two stills with the same hash mean the second
proves nothing the first did not. That is not always a bug: two routes that
render the same empty state produce honest identical frames, and a video is
the wrong medium for that claim. Check before publishing two of them as if
they showed different things:

```bash
shasum <a>.png <b>.png
```

## 2. Before / after, for every surface that already existed

Only a surface this PR **introduces** is exempt, and one PR often has both
kinds: a new screen plus one control dropped into an existing sheet is one
greenfield surface and one changed surface — the changed one still owes a
before.

The before comes from the **base branch**, never from cropping or re-staging
the after shot. Check the base out in a scratch worktree, bring the stack up
against it, and run the same scenario:

```bash
git worktree add <scratch>/base-shot <base-branch>
# launch + drive there, then take the still through the same command
git worktree remove <scratch>/base-shot
```

Name the pair `<name>-before.png` / `<name>-after.png` and put both through
the identical still command, so the two frames are the same width — a
reviewer comparing two differently-scaled screenshots reads the scaling as a
change.

## 3. Attach into the PR body

`gh pr edit --attach` uploads a local file and rewrites the body's reference
to point at the uploaded asset, so the media lives in GitHub's own attachment
store. No media branch, no raw URLs, nothing to clean up when the PR closes.

Its version gate, alt-text rules, format limits and the authenticated check
that proves an upload landed are in
[`references/gh-attach.md`](references/gh-attach.md). Read it before the first
attach: several of its rules bite silently.

```bash
gh pr edit <n> --body-file <body.md> \
  --attach './checkout.mp4' \
  --attach './events.png#Events list rendering three published events'
```

Done when every surface the diff touches carries the form its proof needs,
MP4 for an interaction and a still for a state; every surface that already
existed also has a before from the base branch at the same width; and every
asset answers 200 to the authenticated check.
