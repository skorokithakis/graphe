// SPDX-License-Identifier: AGPL-3.0-or-later

package post_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skorokithakis/graphe/internal/post"
)

// sampleMarkdown is a representative post with YAML frontmatter and a fenced
// code block. The body after the closing "---\n" is what Source must equal.
const sampleMarkdown = `---
title: My Test Post
date: 2026-01-01
---
# Heading

Some paragraph text.

` + "```" + `go
package main

func main() {}
` + "```" + `
`

// expectedSource is the exact bytes that should appear in Post.Source after
// frontmatter stripping. The anchor matcher will substring-search against this.
const expectedSource = `# Heading

Some paragraph text.

` + "```" + `go
package main

func main() {}
` + "```" + `
`

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

func TestLoad_FrontmatterStrippedFromSource(t *testing.T) {
	path := writeTempFile(t, "test.md", sampleMarkdown)

	p, err := post.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if p.Source != expectedSource {
		t.Errorf("Source mismatch.\ngot:  %q\nwant: %q", p.Source, expectedSource)
	}
}

func TestLoad_TitleFromFrontmatter(t *testing.T) {
	path := writeTempFile(t, "test.md", sampleMarkdown)

	p, err := post.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if p.Title != "My Test Post" {
		t.Errorf("Title = %q, want %q", p.Title, "My Test Post")
	}
}

func TestLoad_HTMLNonEmpty(t *testing.T) {
	path := writeTempFile(t, "test.md", sampleMarkdown)

	p, err := post.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if string(p.HTML) == "" {
		t.Error("HTML is empty")
	}
}

func TestLoad_HTMLContainsSyntaxHighlighting(t *testing.T) {
	path := writeTempFile(t, "test.md", sampleMarkdown)

	p, err := post.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	// Chroma wraps highlighted code in a <pre> with a class attribute.
	html := string(p.HTML)
	if !strings.Contains(html, "<pre") {
		t.Errorf("HTML does not contain a <pre> element; got:\n%s", html)
	}
}

func TestLoad_TitleFallsBackToH1(t *testing.T) {
	content := "# My H1 Title\n\nSome text.\n"
	path := writeTempFile(t, "article.md", content)

	p, err := post.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if p.Title != "My H1 Title" {
		t.Errorf("Title = %q, want %q", p.Title, "My H1 Title")
	}
}

func TestLoad_TitleFallsBackToFilename(t *testing.T) {
	content := "Just some text with no heading.\n"
	path := writeTempFile(t, "my-article.md", content)

	p, err := post.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if p.Title != "my-article" {
		t.Errorf("Title = %q, want %q", p.Title, "my-article")
	}
}

func TestLoad_NoFrontmatterSourceUnchanged(t *testing.T) {
	content := "# Plain post\n\nNo frontmatter here.\n"
	path := writeTempFile(t, "plain.md", content)

	p, err := post.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if p.Source != content {
		t.Errorf("Source mismatch.\ngot:  %q\nwant: %q", p.Source, content)
	}
}

func TestLoad_TOMLFrontmatterStripped(t *testing.T) {
	content := "+++\ntitle = \"TOML Post\"\n+++\n# Body\n"
	path := writeTempFile(t, "toml.md", content)

	p, err := post.Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if p.Source != "# Body\n" {
		t.Errorf("Source = %q, want %q", p.Source, "# Body\n")
	}
	// TOML title extraction is not implemented; falls back to H1.
	if p.Title != "Body" {
		t.Errorf("Title = %q, want %q", p.Title, "Body")
	}
}
