// SPDX-License-Identifier: AGPL-3.0-or-later

// Package post handles the markdown and frontmatter pipeline.
package post

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
)

// Post holds the parsed content of a single markdown file.
type Post struct {
	// Title is derived from frontmatter, first H1, or filename stem, in that order.
	Title string
	// Source is the raw markdown bytes after frontmatter has been stripped.
	// Downstream anchor matching does substring searches against this field,
	// so it must be byte-for-byte identical to what was in the file after the
	// frontmatter delimiter line.
	Source string
	// HTML is the rendered output.
	HTML template.HTML
}

// frontmatterResult holds the extracted frontmatter fields and the remaining body.
type frontmatterResult struct {
	title string
	body  []byte
}

var (
	// yamlTitlePattern matches a bare "title: some value" line in YAML frontmatter.
	// We only handle the simple single-line case; quoted or multiline values fall
	// through to the H1/filename fallback.
	yamlTitlePattern = regexp.MustCompile(`(?m)^title:\s*(.+)$`)

	// h1Pattern matches the first ATX or setext H1 in the markdown body.
	h1Pattern = regexp.MustCompile(`(?m)^#\s+(.+)$`)
)

// stripFrontmatter removes leading YAML (--- delimited) or TOML (+++ delimited)
// frontmatter from content and returns the extracted fields plus the remaining body.
// If no frontmatter is found, body is the original content unchanged.
func stripFrontmatter(content []byte) frontmatterResult {
	delimiter, rest := detectFrontmatter(content)
	if delimiter == "" {
		return frontmatterResult{body: content}
	}

	// Find the closing delimiter.
	closing := []byte("\n" + delimiter + "\n")
	index := bytes.Index(rest, closing)
	if index == -1 {
		// Closing delimiter not found; treat the whole file as body.
		return frontmatterResult{body: content}
	}

	header := rest[:index]
	// body starts after the closing delimiter line (skip the leading newline too).
	body := rest[index+len(closing):]

	result := frontmatterResult{body: body}

	// Only attempt title extraction for YAML frontmatter.
	if delimiter == "---" {
		if match := yamlTitlePattern.FindSubmatch(header); match != nil {
			// Strip optional surrounding quotes that some authors add.
			title := strings.TrimSpace(string(match[1]))
			title = strings.Trim(title, `"'`)
			result.title = title
		}
	}

	return result
}

// detectFrontmatter checks whether content starts with a known frontmatter
// delimiter and returns the delimiter string and the content after the opening
// delimiter line. Returns ("", nil) when no frontmatter is present.
func detectFrontmatter(content []byte) (delimiter string, rest []byte) {
	for _, delim := range []string{"---", "+++"} {
		prefix := []byte(delim + "\n")
		if bytes.HasPrefix(content, prefix) {
			return delim, content[len(prefix):]
		}
	}
	return "", nil
}

// markdownRenderer is a shared goldmark instance configured with GFM extensions
// and chroma-based syntax highlighting using the "github" style, which is
// readable on a light background.
//
// WithUnsafe is required so that the server can insert raw <mark> HTML tags
// into the markdown source before rendering (for comment anchor highlighting).
// The content being rendered is always a local file, never user-submitted input,
// so allowing raw HTML is safe in this context.
var markdownRenderer = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		highlighting.NewHighlighting(
			highlighting.WithStyle("github"),
			highlighting.WithFormatOptions(
				chromahtml.WithClasses(false),
			),
		),
	),
	goldmark.WithRendererOptions(
		goldmarkhtml.WithUnsafe(),
	),
)

// Render converts a markdown source string to HTML using the shared goldmark
// renderer. The server calls this directly after inserting anchor markers into
// the source; Load calls it for the unmodified case.
func Render(source string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := markdownRenderer.Convert([]byte(source), &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// Load reads the markdown file at path, strips any frontmatter, renders the
// remaining markdown to HTML, and returns a populated Post.
func Load(path string) (*Post, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fm := stripFrontmatter(raw)

	rendered, err := Render(string(fm.body))
	if err != nil {
		return nil, err
	}

	post := &Post{
		Source: string(fm.body),
		HTML:   rendered,
	}

	// Title resolution: frontmatter > first H1 > filename stem.
	switch {
	case fm.title != "":
		post.Title = fm.title
	case h1Pattern.Match(fm.body):
		match := h1Pattern.FindSubmatch(fm.body)
		post.Title = strings.TrimSpace(string(match[1]))
	default:
		stem := filepath.Base(path)
		stem = strings.TrimSuffix(stem, filepath.Ext(stem))
		post.Title = stem
	}

	return post, nil
}
