---
title: A sample post for testing
date: 2026-05-03
---

## Introduction

This post exists solely to provide stable fixture data for the graphe test suite.
It contains the structural elements the tests need: headings, a code block, and a
paragraph long enough to anchor two independent comments to two different sentences.

## Code example

The following snippet prints a greeting:

```python
def greet(name: str) -> str:
    return f"Hello, {name}!"

print(greet("world"))
```

## Discussion

Graphe stores review comments as anchored substrings of the post source, so each
anchor must appear exactly once in the document. The start anchor marks where a
comment begins, and the end anchor marks where it finishes. Reviewers can therefore
point at any contiguous span of text without modifying the file itself. This design
keeps the markdown source clean and makes comments easy to rebase when the prose
changes only slightly.
