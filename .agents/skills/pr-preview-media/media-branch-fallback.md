# Media branch fallback

The path for `gh` older than 2.99.0, and for GitHub Enterprise Server, where
`--attach` does not exist. On a new enough `gh` the files upload into the body
instead and none of this applies. Check before reaching for it:

```bash
gh --version
```

Media rides an orphan branch named `pr-<n>-media`, never merged:

```bash
git worktree add --orphan -b pr-<n>-media <scratch>/media-branch
# copy media in, add a README naming the source run and "never merge"
git commit --no-verify -m "chore: preview media for PR #<n>"
git push origin pr-<n>-media && git worktree remove <scratch>/media-branch
```

`--no-verify` earns its place here and essentially nowhere else: a repo's
pre-commit hooks generally run lint, typecheck and tests through a toolchain,
and an orphan branch holding only media has no `package.json` and nothing for
those gates to check.

Image URLs MUST use the same-repo form
`https://github.com/<owner>/<repo>/raw/pr-<n>-media/<file>.png`. On a **private**
repo `raw.githubusercontent.com` 404s for GitHub's image proxy, so every image
renders blank; the `github.com/…/raw/…` form is rewritten into per-viewer
signed URLs and works for both visibilities.

Say in the PR body that the media is served from the never-to-be-merged
`pr-<n>-media` branch and should be deleted once the PR closes — an attached
file needs no such cleanup, so whoever closes the PR has to be told which kind
this is.
