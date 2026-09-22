# Uploading media with `gh --attach`

The single source of truth for the flag. `create-pr` and `pr-preview-media`
both publish through it; this file holds the constraints so a limit that
changes is a one-place edit.

## Version gate

`--attach` landed in **gh 2.99.0**. Below that, and on GitHub Enterprise
Server, the flag does not exist and the fallback applies:

```bash
gh --version
```

Fallback: [`../media-branch-fallback.md`](../media-branch-fallback.md).

## How it works

Reference each file by its **local path** in the body markdown, then attach
it. GitHub uploads the file, rewrites that path to a
`github.com/user-attachments/assets/<uuid>` URL, and keeps the alt text. Run
`gh` **from the directory holding the media** so `./name.mp4` resolves.

```bash
# body contains:  ![checkout flow](./checkout.mp4) and ![events](./events.png)
gh pr edit <n> --body-file <body.md> \
  --attach './checkout.mp4' \
  --attach './events.png#Events list rendering three published events'
```

Available on `gh pr create`, `gh pr edit` and `gh pr comment`.

## Rules

- Alt text follows the path after `#`. Omit it and the filename is used.
- **Video takes no alt text.** `--attach './flow.mp4#…'` fails with `cannot
  set alt text on video`; pass the bare path. Write the body reference as
  `![what it shows](./flow.mp4)` anyway — gh replaces the whole reference with
  a bare asset URL on its own line, which is what makes GitHub draw a player.
  Images keep `![alt](url)`, so their `#` suffix is worth writing.
- An attachment the body does not reference is appended to the end instead.
- Repeat the flag once per file, up to 50 per command.
- Without a body flag the PR keeps the body it has, and attachments append.

## Limits

PNG, JPEG, GIF, WebP, SVG, MP4, MOV and WebM upload. Images cap at 10 MB;
video at 10 MB on Free and 100 MB on paid plans.

## Verifying the upload

**A partial upload still rewrites the body** and exits non-zero after printing
the URL, so read the status and then check each asset — a body that looks
right can carry a broken asset.

**On a private repo the assets are access-controlled and an anonymous fetch
answers 404 for a perfectly good file.** Check as the viewer does, with
credentials:

```bash
gh pr view <n> --json body -q .body \
  | grep -o 'https://github.com/user-attachments/assets/[^)[:space:]]*' \
  | while read -r u; do
      curl -s -o /dev/null -w "%{http_code} %{content_type} $u\n" -L \
        -H "Authorization: token $(gh auth token)" "$u"
    done
```

Expect `200 image/png` or `200 video/mp4` per asset.
