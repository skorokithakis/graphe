# graphe

> [*γραφή*], writing

Render a markdown post in the browser with margin-note review comments,
designed for human-LLM collaborative prose review.

graphe serves a single markdown file as a Tufte-style page and overlays
reviewer comments as post-it style margin notes. Comments live in a sidecar
JSON file and are managed through a small CLI, which means an LLM can add,
list, edit, and delete them directly. The browser reloads automatically
whenever the post or its comments change.

## Install

```sh
go install github.com/skorokithakis/graphe/cmd/graphe@latest
```

## Quickstart

In one terminal, start the preview server:

```sh
graphe serve post.md
```

Open <http://127.0.0.1:7290>. In another terminal (or from an LLM), add a
comment:

```sh
graphe comment add post.md \
  --start "The timeline is optimistic" \
  --end   "account for contingencies." \
  --body  "Consider adding a 20% buffer to each milestone."
```

The browser reloads and the comment appears as a margin note next to the
highlighted span. Resolve a comment by deleting it:

```sh
graphe comment delete post.md c-a1b2
```

### Other commands

- **List** all comments with their IDs and anchors:
  ```sh
  graphe comment list post.md
  ```
- **Edit** an existing comment (any combination of `--start`, `--end`, `--body`):
  ```sh
  graphe comment edit post.md c-a1b2 --body "Revised suggestion."
  ```

## Use with an LLM

Point the LLM at the file and ask it to review. The single command it needs
to learn the workflow is:

```sh
graphe help
```

That prints a self-contained overview covering the anchor model, every
subcommand, and tips for writing useful comments.

## License

[AGPL-3.0-or-later](LICENSE).
