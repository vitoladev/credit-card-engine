---
name: devcontainer
description: |
  Per-worktree container workflow: toolchain commands run inside, git runs on
  the host. Use when entering or leaving a worktree, running build/test/lint
  commands, or debugging container mount and cache errors.
---

# Per-worktree devcontainers

Each git worktree gets its own container holding its own dependency tree, so
parallel agents never collide. **Toolchain commands run inside the container;
git commands run on the host.**

This repo carries a minimal reference implementation you can copy into a
project and grow: `.devcontainer/devcontainer.json` plus the three lifecycle
scripts in `scripts/devcontainer/`. It is deliberately bare — a base image and
the worktree plumbing, no language toolchains — because the plumbing is the
portable part.

## Adapting it to a project

- **Image.** Swap the base image for one carrying the project's toolchain, or
  point `devcontainer.json` at a `Dockerfile` you own. Add tooling there, never
  as ad-hoc `apt install`s inside a running container — an ad-hoc install
  vanishes on the next rebuild and is invisible to the next agent.
- **Install step.** Add `"postCreateCommand"` for the dependency install. It
  belongs *inside* the container: a lockfile install run on the host writes
  host-platform binaries into `node_modules` (or its equivalent), which then
  fail in a Linux container.
- **Backing services.** When the project needs a database, queue, or emulator,
  switch `"image"` for `"dockerComposeFile"` + `"service"`. `down.sh` already
  handles both shapes — it tears down the whole compose project when there is
  one, and removes the lone container when there is not.
- **Ports.** Publish fixed host ports only for the main worktree; give linked
  worktrees port `0` so parallel containers never fight over the same number.
- **Caches on bind mounts.** Build tools that rename-over-existing files
  (bundlers, some test runners) fail on virtiofs bind mounts. Mount those cache
  directories as `tmpfs` in compose rather than fighting the error.

## 1. Setup — entering a worktree

Create worktrees at a convention path so Docker's file sharing covers them:

```sh
git worktree add ~/development/<repo>-worktrees/<branch> -b <branch>
```

Then, from inside the worktree:

```sh
scripts/devcontainer/up.sh
```

Idempotent — run it whenever unsure the container exists. It starts a
container keyed to this worktree's absolute path and mounts the main repo's
`.git` directory at the same path inside the container, so git works in there
too (for the main worktree that is a harmless self-mount). Setup is complete
when it prints `Devcontainer ready for: <path>`.

## 2. Running commands

Prefix every toolchain command with `scripts/devcontainer/exec.sh`:

```sh
scripts/devcontainer/exec.sh <build command>
scripts/devcontainer/exec.sh bash -c 'curl -s localhost:<port>/health'
```

Servers started in the container are reachable only from inside it, except
ports the config publishes. Verify anything else with an `exec.sh ... curl`, as
in the second example — a host-side curl against an unpublished port reports a
failure the service does not have.

## 3. Teardown — leaving a worktree

Remove the container before the worktree:

```sh
scripts/devcontainer/down.sh   # from inside the worktree
cd <main-repo> && git worktree remove <path>
```

Teardown is complete when both the container and the worktree are gone.

## Reference: stray containers

A worktree deleted without `down.sh` leaves its container behind. List
candidates and remove any whose path no longer exists:

```sh
docker ps -a --filter "label=devcontainer.local_folder" \
  --format '{{.ID}}  {{.Label "devcontainer.local_folder"}}'
docker rm -f <id>
```

Containers labeled with paths outside this repo's worktrees belong to other
projects — leave them.

## Reference: known failure modes

- `Error fetching image details: No manifest found for <image>` during `up.sh`
  → benign. The CLI logs it while probing for metadata, then pulls and starts
  the container anyway. Judge the run by its final `{"outcome":"success"}`.
- `bind source path does not exist` on `up.sh` → the worktree path isn't shared
  with Docker Desktop (Settings → Resources → File Sharing). Keep worktrees
  under a path that is shared by default.
- A build tool reports `File exists (os error 17)`, `EEXIST`, or `ENOTDIR`
  writing into its own cache → that directory is on the virtiofs bind mount,
  which rejects rename-over-existing writes. Mount it as `tmpfs`, then recreate
  the container.
- Permission denied writing a cache directory that lives outside the workspace
  → pin the cache inside `${containerWorkspaceFolder}` via `containerEnv`.
- Any change to `.devcontainer/devcontainer.json` or the compose file applies
  only to newly created containers — `down.sh` then `up.sh` to pick it up.
