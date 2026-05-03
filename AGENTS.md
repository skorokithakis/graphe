# AGENTS.md

Notes for agents working in this repo. Read once per session.

## What this is

`graphe` is a Go CLI that serves a markdown file as a Tufte-style web page
and overlays reviewer comments as margin notes. Comments live in a sidecar
file next to the markdown; the browser auto-reloads on changes via SSE.

License: AGPL-3.0-or-later. Every Go file starts with the SPDX header.

## Layout

- `cmd/graphe/main.go` — entrypoint, just calls into `internal/cli`.
- `internal/cli/` — cobra commands (`root`, `serve`, `comment`, `help`).
  - `help.go` holds a long-form `overviewText` constant aimed at LLMs.
- `internal/post/` — markdown loading, frontmatter stripping, goldmark
  rendering. `post.Source` is byte-for-byte the markdown body after
  frontmatter; comment anchors are matched as literal substrings against
  it, so do not normalise it.
- `internal/review/` — comment store. Loads/saves the sidecar JSON,
  validates anchors (each must match exactly once and end must not fall
  inside start), generates `c-xxxx` IDs.
- `internal/server/` — HTTP server, SSE broadcaster, fsnotify watcher.
  - `watcher.go` watches the parent directory (not the files directly)
    so atomic-save renames don't drop the watch, and so a sidecar
    created after startup is still picked up. Events are debounced
    150 ms.
- `testdata/` — `sample.md` + `sample.graphe` fixture.

## Conventions

- Standard Go style. Built-in collection types, no abbreviations
  (`commentID`, not `cmtId`).
- Comments explain *why*, not *what*. Several existing comments document
  rejected alternatives — keep that style.
- Errors are returned and wrapped (`fmt.Errorf("...: %w", err)`); CLI
  layer prints them. Don't swallow with try/catch-style defensiveness.
- Cobra commands live as package-level vars wired up in `init()`. Flags
  declared in the same `init()`.
- Tests use `t.TempDir()` and write fixtures inline; no test framework
  beyond the stdlib.

## Sidecar file

`<stem>.graphe` next to the markdown file (e.g. `post.md` → `post.graphe`).
Shape:

```json
{ "comments": [ { "id": "c-a1b2", "start": "...", "end": "...", "body": "...", "created_at": "..." } ] }
```

The path is derived in three places that must stay in sync:
- `internal/review/review.go` — `sidecarPathFor`
- `internal/server/watcher.go` — `sidecarBasename`
- Documentation strings in `internal/cli/comment.go` and
  `internal/cli/help.go`, plus the README.

## Build, test, run

- Build: `go build ./...`
- Test: `go test ./...`
- Format/vet: standard `gofmt`/`go vet`. No pre-commit hooks configured.
- Run locally: `go run ./cmd/graphe serve testdata/sample.md`. The server
  listens on `127.0.0.1:7290` by default. Don't start the dev server
  yourself — ask the human if you need it running.

## Things easy to get wrong

- `post.Source` byte fidelity: any change to frontmatter handling must
  preserve exact bytes after the closing delimiter, or anchor matching
  breaks silently for existing comments.
- Anchor uniqueness uses an overlapping count (`countOverlapping`), not
  `strings.Count`, so `"aa"` in `"aaa"` is two matches.
- `goldmark` is configured `WithUnsafe()` because the server inserts raw
  `<mark>` tags around anchored ranges before rendering. Anchors inside
  fenced code blocks won't highlight (goldmark escapes the inserted HTML).
- The watcher ignores `Chmod`-only events; don't add file-permission
  changes as a reload trigger.
