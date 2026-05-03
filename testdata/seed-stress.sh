#!/bin/sh
# Seed testdata/stress.md with one comment per broken-case block instance.
#
# Every construct appears twice in stress.md:
#   - "reference": no comment seeded. Acts as the visual baseline showing
#     what correct rendering looks like.
#   - "broken": a whole-line anchor whose --start begins at the very start
#     of the line and includes the block prefix (e.g. "# ", "> ", "- ").
#     This is the bug-triggering case that gra-bkpit will fix.
#
# Run from the repo root:
#   bash testdata/seed-stress.sh
# Then open the served page to compare broken vs reference rendering:
#   go run ./cmd/graphe serve testdata/stress.md

set -eu

MD=testdata/stress.md

go run ./cmd/graphe comment clear "$MD"

# ---------------------------------------------------------------------------
# ATX h1
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "# H1 broken" \
  --end   "# H1 broken" \
  --body  "h1: whole-line anchor includes leading #"

# ---------------------------------------------------------------------------
# ATX h2
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "## H2 broken" \
  --end   "## H2 broken" \
  --body  "h2: whole-line anchor includes leading ##"

# ---------------------------------------------------------------------------
# ATX h3
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "### H3 broken" \
  --end   "### H3 broken" \
  --body  "h3: whole-line anchor includes leading ###"

# ---------------------------------------------------------------------------
# ATX h4
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "#### H4 broken" \
  --end   "#### H4 broken" \
  --body  "h4: whole-line anchor includes leading ####"

# ---------------------------------------------------------------------------
# ATX h5
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "##### H5 broken" \
  --end   "##### H5 broken" \
  --body  "h5: whole-line anchor includes leading #####"

# ---------------------------------------------------------------------------
# ATX h6
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "###### H6 broken" \
  --end   "###### H6 broken" \
  --body  "h6: whole-line anchor includes leading ######"

# ---------------------------------------------------------------------------
# Plain paragraph
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "Plain paragraph broken." \
  --end   "Plain paragraph broken." \
  --body  "paragraph: whole-line anchor from line start (no block prefix)"

# ---------------------------------------------------------------------------
# Bullet list
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "- Bullet broken" \
  --end   "- Bullet broken" \
  --body  "bullet: whole-line anchor includes leading -"

# ---------------------------------------------------------------------------
# Ordered list
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "1. Ordered broken" \
  --end   "1. Ordered broken" \
  --body  "ordered: whole-line anchor includes leading 1."

# ---------------------------------------------------------------------------
# Blockquote
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "> Blockquote broken." \
  --end   "> Blockquote broken." \
  --body  "blockquote: whole-line anchor includes leading >"

# ---------------------------------------------------------------------------
# Nested blockquote — outer line
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "> Nested outer broken." \
  --end   "> Nested outer broken." \
  --body  "nested-outer: whole-line anchor includes leading >"

# ---------------------------------------------------------------------------
# Nested blockquote — inner line
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "> > Nested inner broken." \
  --end   "> > Nested inner broken." \
  --body  "nested-inner: whole-line anchor includes leading > >"

# ---------------------------------------------------------------------------
# Heading inside blockquote
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "> ## Heading-in-blockquote broken" \
  --end   "> ## Heading-in-blockquote broken" \
  --body  "heading-in-bq: whole-line anchor includes leading > ##"

# ---------------------------------------------------------------------------
# List inside blockquote
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "> - List-in-blockquote broken" \
  --end   "> - List-in-blockquote broken" \
  --body  "list-in-bq: whole-line anchor includes leading > -"

# ---------------------------------------------------------------------------
# Indented heading (1 leading space)
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start " # Indented-1 broken" \
  --end   " # Indented-1 broken" \
  --body  "indented-1: whole-line anchor includes leading space + #"

# ---------------------------------------------------------------------------
# Indented heading (2 leading spaces)
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "  ## Indented-2 broken" \
  --end   "  ## Indented-2 broken" \
  --body  "indented-2: whole-line anchor includes 2 spaces + ##"

# ---------------------------------------------------------------------------
# Indented heading (3 leading spaces)
# ---------------------------------------------------------------------------
go run ./cmd/graphe comment add "$MD" \
  --start "   ### Indented-3 broken" \
  --end   "   ### Indented-3 broken" \
  --body  "indented-3: whole-line anchor includes 3 spaces + ###"

# ---------------------------------------------------------------------------
# Setext heading
# ---------------------------------------------------------------------------
# The anchor spans both the text line and the underline so --start begins at
# the very start of the heading text. The reference uses --- (setext h2) and
# the broken uses === (setext h1) so the two underlines are distinct strings
# and neither is a substring of the other.
go run ./cmd/graphe comment add "$MD" \
  --start "Setext broken" \
  --end   "=============" \
  --body  "setext: anchor spans text line + === underline"

# ---------------------------------------------------------------------------
# Fenced code block
# ---------------------------------------------------------------------------
# The reference block uses ```sh and the broken block uses ```python so each
# opening fence is unique. The closing ``` is not used as an anchor because
# it appears as a prefix of both opening fence lines.
go run ./cmd/graphe comment add "$MD" \
  --start '```python' \
  --end   'print(y)' \
  --body  "fenced: whole-line anchor includes opening fence"

echo "next: go run ./cmd/graphe serve testdata/stress.md"
