// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server serves the rendered post with Tufte-style margin comments.
package server

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/skorokithakis/graphe/internal/post"
	"github.com/skorokithakis/graphe/internal/review"
)

//go:embed static/style.css static/main.js static/page.html
var staticFiles embed.FS

// pageTemplate is parsed once at startup from the embedded page.html.
var pageTemplate = template.Must(
	template.New("page").Parse(mustReadStatic("static/page.html")),
)

// cssContent and jsContent are inlined into the page to avoid extra round-trips.
var (
	cssContent = mustReadStatic("static/style.css")
	jsContent  = mustReadStatic("static/main.js")
)

func mustReadStatic(name string) string {
	data, err := staticFiles.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("reading embedded file %s: %v", name, err))
	}
	return string(data)
}

// renderedComment is a comment whose body has been converted to HTML, ready for
// the template.
type renderedComment struct {
	ID       string
	Body     template.HTML
	Orphaned bool
}

// renderedPage is the data passed to pageTemplate.
type renderedPage struct {
	Title    string
	HTML     template.HTML
	CSS      template.CSS
	JS       template.JS
	Comments []renderedComment
}

// commentOffset records the last-known byte offsets for one comment's anchor
// span within the markdown source. These are tracked in memory across reloads
// so that a diff-based translation can keep comments visible after edits.
type commentOffset struct {
	start int
	end   int
}

// Server loads a markdown post and its review comments and serves them over HTTP.
type Server struct {
	mdPath string

	// mu guards rendered, hasPreviousSource, prevSource, and offsets; Reload
	// writes them, HTTP handlers read rendered.
	mu       sync.RWMutex
	rendered renderedPage

	// hasPreviousSource is false until the first successful Reload completes.
	// Using an explicit boolean avoids treating a genuinely empty markdown body
	// as "no previous source", which would incorrectly skip diff translation on
	// the second reload.
	hasPreviousSource bool

	// prevSource is the markdown source from the previous successful Reload.
	// Only valid when hasPreviousSource is true.
	prevSource string

	// offsets maps each comment ID to its last-known byte offsets in the
	// markdown source. Populated on bootstrap; updated on every reload via
	// diff translation. Never persisted to disk.
	offsets map[string]commentOffset

	// mutateMu serializes load-modify-save against the sidecar so concurrent
	// DELETE clicks do not lose updates. It is separate from mu, which guards
	// rendered state; holding mutateMu does not block reads.
	mutateMu sync.Mutex

	// broadcaster distributes SSE events to connected clients.
	// The live-reload ticket (gra-dmtfx) will send real events through this;
	// for now the SSE handler only uses it for the heartbeat loop.
	broadcaster *broadcaster
}

// New creates a Server for the markdown file at mdPath and performs the initial
// load. Returns an error if the file cannot be read or rendered.
func New(mdPath string) (*Server, error) {
	// Ensure the cache directory exists before Watch calls watcher.Add on it.
	// Doing this here rather than inside Watch keeps the error surface at
	// startup rather than mid-run.
	if err := review.EnsureCacheDir(); err != nil {
		return nil, err
	}

	s := &Server{
		mdPath:      mdPath,
		broadcaster: newBroadcaster(),
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload re-reads the markdown file and its sidecar comment file from disk,
// rebuilds the rendered page, and atomically replaces the served content.
// It is safe to call concurrently with HTTP serving.
func (s *Server) Reload() error {
	loadedPost, err := post.Load(s.mdPath)
	if err != nil {
		return fmt.Errorf("loading post: %w", err)
	}

	store, err := review.Load(s.mdPath)
	if err != nil {
		return fmt.Errorf("loading review store: %w", err)
	}

	comments := store.List()
	currSource := loadedPost.Source

	s.mu.Lock()
	defer s.mu.Unlock()

	// Initialise the offsets map on first use.
	if s.offsets == nil {
		s.offsets = make(map[string]commentOffset)
	}

	// Translate any previously recorded offsets through the diff from prevSource
	// to currSource. This must happen before buildAnnotatedPage so that the
	// translated offsets are available for all comments.
	if s.hasPreviousSource {
		s.offsets = translateOffsets(s.prevSource, currSource, s.offsets)
	}

	// Build the annotated HTML: insert <mark> or orphan-pin elements into the
	// markdown source at the byte offsets of each comment's anchor span, then
	// render. Bootstrap comments (no recorded offset) use strict anchor lookup.
	annotatedHTML, renderedComments, newOffsets, err := buildAnnotatedPage(loadedPost, comments, s.offsets)
	if err != nil {
		return fmt.Errorf("building annotated page: %w", err)
	}

	// Merge newly bootstrapped offsets into the tracked set. We do not replace
	// the whole map because translateOffsets already updated existing entries.
	for id, offset := range newOffsets {
		s.offsets[id] = offset
	}

	s.prevSource = currSource
	s.hasPreviousSource = true

	page := renderedPage{
		Title:    loadedPost.Title,
		HTML:     annotatedHTML,
		CSS:      template.CSS(cssContent),
		JS:       template.JS(jsContent),
		Comments: renderedComments,
	}

	s.rendered = page

	return nil
}

// translateOffsets applies a character-level diff from prevSource to currSource
// to each recorded offset, returning a new map with the translated values.
//
// Translation rules per the ticket spec:
//   - Equal blocks: offsets within them shift by the accumulated delta.
//   - Inserts before an offset: shift the offset right by the insert length.
//   - Deletes that contain an offset: collapse the offset to the deletion point.
//
// Offsets past len(currSource) are clamped to len(currSource).
func translateOffsets(prevSource, currSource string, offsets map[string]commentOffset) map[string]commentOffset {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(prevSource, currSource, false)

	result := make(map[string]commentOffset, len(offsets))
	for id, offset := range offsets {
		result[id] = commentOffset{
			start: translateOffset(diffs, offset.start),
			end:   translateOffset(diffs, offset.end),
		}
	}
	return result
}

// translateOffset maps a single byte offset from the old source to the new
// source by walking the diff list and accumulating position changes.
func translateOffset(diffs []diffmatchpatch.Diff, position int) int {
	oldPos := 0
	newPos := 0

	for _, diff := range diffs {
		length := len(diff.Text)

		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			if oldPos+length > position {
				// The position falls inside this equal block; shift by the
				// accumulated delta (newPos - oldPos).
				return newPos + (position - oldPos)
			}
			oldPos += length
			newPos += length

		case diffmatchpatch.DiffInsert:
			// An insert before or at the position shifts it right. We use
			// strict "before" (newPos <= translated position) rather than
			// "at or before" to avoid double-counting when an insert and a
			// delete land at the same old position.
			newPos += length

		case diffmatchpatch.DiffDelete:
			if oldPos+length > position {
				// The deletion swallows the position; collapse to the
				// deletion point in the new source.
				return newPos
			}
			oldPos += length
		}
	}

	// Position was at or past the end of the old source; clamp to new length.
	return newPos
}

// CloseSubscribers closes all live SSE subscriber channels so the /events
// handlers return promptly. Wire this into http.Server.RegisterOnShutdown:
// Shutdown does not cancel in-flight request contexts, so without this the
// long-lived SSE connections keep Shutdown blocked until its timeout expires.
func (s *Server) CloseSubscribers() {
	s.broadcaster.closeAll()
}

// SubscribeForTest returns a channel that receives SSE event names. It is
// intended only for use in tests; production code should use the SSE endpoint.
func (s *Server) SubscribeForTest() chan string {
	return s.broadcaster.subscribe()
}

// UnsubscribeForTest removes the channel returned by SubscribeForTest.
func (s *Server) UnsubscribeForTest(channel chan string) {
	s.broadcaster.unsubscribe(channel)
}

// commentIDPattern matches the c-xxxx format used for comment IDs. The four
// characters after the hyphen are lowercase alphanumeric, matching the alphabet
// used by review.generateID. Validating before touching disk avoids a class of
// path-traversal-style issues even though this is a single-user local tool.
var commentIDPattern = regexp.MustCompile(`^c-[a-z0-9]{4}$`)

// Handler returns an http.Handler that serves the post and SSE endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("DELETE /comments/{id}", s.handleDeleteComment)
	return mux
}

func (s *Server) handleDeleteComment(writer http.ResponseWriter, request *http.Request) {
	commentID := request.PathValue("id")

	if !commentIDPattern.MatchString(commentID) {
		http.Error(writer, "malformed comment id", http.StatusBadRequest)
		return
	}

	// Serializes load-modify-save against the sidecar so concurrent DELETE
	// clicks do not lose updates.
	s.mutateMu.Lock()
	defer s.mutateMu.Unlock()

	store, err := review.Load(s.mdPath)
	if err != nil {
		log.Printf("loading review store for delete: %v", err)
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}

	// Check existence before calling Delete so we can distinguish "not found"
	// from a disk write failure, both of which Delete returns as a plain error.
	found := false
	for _, comment := range store.List() {
		if comment.ID == commentID {
			found = true
			break
		}
	}
	if !found {
		http.Error(writer, "comment not found", http.StatusNotFound)
		return
	}

	if err := store.Delete(commentID); err != nil {
		log.Printf("deleting comment %s: %v", commentID, err)
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}

	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIndex(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}

	s.mu.RLock()
	page := s.rendered
	s.mu.RUnlock()

	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, page); err != nil {
		log.Printf("rendering page template: %v", err)
		http.Error(writer, "internal server error", http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(buf.Bytes())
}

func (s *Server) handleEvents(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)

	flusher, canFlush := writer.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	events := s.broadcaster.subscribe()
	defer s.broadcaster.unsubscribe(events)

	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			fmt.Fprintf(writer, "event: %s\ndata: \n\n", event)
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

// anchorSpan records the resolved byte offsets for one comment's anchor in the
// markdown source.
type anchorSpan struct {
	comment    review.Comment
	startIndex int
	endIndex   int // index of the first byte of comment.End
	orphaned   bool
}

// buildAnnotatedPage inserts <mark> elements (for anchored comments) or
// <span class="orphan-pin"> elements (for orphaned comments) into the markdown
// source at each comment's anchor span, renders the result, and returns the
// HTML along with the rendered comment bodies and any newly bootstrapped offsets.
//
// The knownOffsets map contains pre-translated byte offsets from the previous
// reload. Comments absent from knownOffsets are bootstrapped via strict anchor
// lookup (the original behaviour). Newly bootstrapped offsets are returned in
// the third return value so the caller can merge them into the tracked set.
//
// Comments whose anchors cannot be located in the source are skipped with a
// log warning rather than causing a hard error, so a stale comment does not
// break the whole page.
//
// Caveat: if an anchor span crosses a markdown block boundary (e.g., spans two
// paragraphs), the inserted <mark> tags may produce invalid HTML because
// goldmark will close the block element before the closing </mark> is emitted.
// We accept this risk for now; in practice anchors are expected to stay within
// a single paragraph.
func buildAnnotatedPage(loadedPost *post.Post, comments []review.Comment, knownOffsets map[string]commentOffset) (template.HTML, []renderedComment, map[string]commentOffset, error) {
	source := loadedPost.Source

	// Build a set of line-start byte offsets that fall inside fenced code blocks
	// so we can skip anchors that land there. A simple state machine is sufficient
	// because we only need to match what goldmark sees, not parse full markdown.
	fencedLineOffsets := fencedCodeBlockLineOffsets(source)

	// bootstrapped collects offsets resolved via strict anchor lookup this
	// reload, so the caller can add them to the persistent tracking map.
	bootstrapped := make(map[string]commentOffset)

	// Resolve byte offsets for each comment. Comments with a known (translated)
	// offset are checked against the current source to determine anchored vs
	// orphaned. Comments without a known offset are bootstrapped via strict
	// anchor lookup.
	var spans []anchorSpan
	for _, comment := range comments {
		if offset, hasOffset := knownOffsets[comment.ID]; hasOffset {
			// Clamp translated offsets to valid range.
			sourceLen := len(source)
			startIndex := offset.start
			endIndex := offset.end
			if startIndex > sourceLen {
				startIndex = sourceLen
			}
			if endIndex > sourceLen {
				endIndex = sourceLen
			}

			// Determine whether the anchor text still matches at the translated
			// position. Both start and end must match for the comment to be
			// considered anchored.
			anchored := sourceMatchesAt(source, startIndex, comment.Start) &&
				sourceMatchesAt(source, endIndex, comment.End)

			// If the translated position now falls inside a fenced code block,
			// goldmark would escape the inserted <mark> tag and show raw HTML to
			// the user. Downgrade to an orphan so the pin is inserted as a plain
			// <span> instead, which goldmark also escapes but is harmless.
			if anchored && fencedLineOffsets[lineStartOffset(source, startIndex)] {
				anchored = false
			}

			spans = append(spans, anchorSpan{
				comment:    comment,
				startIndex: startIndex,
				endIndex:   endIndex,
				orphaned:   !anchored,
			})
			continue
		}

		// Bootstrap: strict anchor lookup (original behaviour).
		startIndex := strings.Index(source, comment.Start)
		if startIndex == -1 {
			log.Printf("comment %s: start anchor %q not found in source, skipping", comment.ID, comment.Start)
			continue
		}
		if countOverlappingOccurrences(source, comment.Start) > 1 {
			log.Printf("comment %s: start anchor %q is ambiguous, skipping", comment.ID, comment.Start)
			continue
		}

		endIndex := strings.Index(source, comment.End)
		if endIndex == -1 {
			log.Printf("comment %s: end anchor %q not found in source, skipping", comment.ID, comment.End)
			continue
		}
		if countOverlappingOccurrences(source, comment.End) > 1 {
			log.Printf("comment %s: end anchor %q is ambiguous, skipping", comment.ID, comment.End)
			continue
		}

		if fencedLineOffsets[lineStartOffset(source, startIndex)] {
			log.Printf("comment %s: anchors inside a code block, skipping", comment.ID)
			continue
		}

		bootstrapped[comment.ID] = commentOffset{start: startIndex, end: endIndex}
		spans = append(spans, anchorSpan{
			comment:    comment,
			startIndex: startIndex,
			endIndex:   endIndex,
			orphaned:   false,
		})
	}

	// Sort by startIndex ascending to detect overlapping spans before we reverse
	// the order for insertion.
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].startIndex < spans[j].startIndex
	})

	// Drop any span that starts before the previous span's end. Overlapping
	// <mark> tags produce invalid HTML and confuse the JS positioning logic.
	// Orphan pins are single-point insertions at startIndex with no extent in
	// the rendered HTML, so their effective end is startIndex itself. Using the
	// stale endIndex for an orphan would incorrectly block a later, valid
	// anchored comment whose startIndex falls between the orphan's translated
	// startIndex and endIndex.
	filtered := spans[:0]
	previousEnd := -1
	for _, span := range spans {
		var spanEnd int
		if span.orphaned {
			spanEnd = span.startIndex
		} else {
			spanEnd = span.endIndex + len(span.comment.End)
		}
		if span.startIndex < previousEnd {
			log.Printf("comment %s overlaps an earlier comment, skipping", span.comment.ID)
			continue
		}
		filtered = append(filtered, span)
		previousEnd = spanEnd
	}
	spans = filtered

	// Sort by startIndex descending so that inserting markers at later offsets
	// first does not shift the byte positions of earlier markers.
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].startIndex > spans[j].startIndex
	})

	// Build the annotated source by inserting raw HTML markers. Goldmark passes
	// raw inline HTML through unchanged when the unsafe option is enabled (which
	// our renderer uses via goldmark.WithExtensions(extension.GFM) — GFM enables
	// raw HTML). The markers are inserted as byte slices to avoid any UTF-8
	// re-encoding issues.
	annotated := []byte(source)
	for _, span := range spans {
		if span.orphaned {
			// Orphaned comments get a zero-width pin so the frontend can
			// position the post-it near where the anchor used to be.
			pin := []byte(fmt.Sprintf(`<span class="orphan-pin" data-comment-id="%s"></span>`, span.comment.ID))
			annotated = insertBytes(annotated, span.startIndex, pin)
		} else {
			endOfSpan := span.endIndex + len(span.comment.End)
			closeTag := []byte("</mark>")
			openTag := []byte(fmt.Sprintf(`<mark class="anchor" data-comment-id="%s">`, span.comment.ID))

			// Insert closing tag first (higher offset) so the opening tag insertion
			// does not shift the closing tag's position.
			annotated = insertBytes(annotated, endOfSpan, closeTag)
			annotated = insertBytes(annotated, span.startIndex, openTag)
		}
	}

	rendered, err := post.Render(string(annotated))
	if err != nil {
		return "", nil, nil, fmt.Errorf("rendering annotated markdown: %w", err)
	}

	// Render each comment body as markdown so the post-it notes support basic
	// formatting (bold, italic, code, etc.).
	var renderedComments []renderedComment
	for _, span := range spans {
		bodyHTML, err := post.Render(span.comment.Body)
		if err != nil {
			log.Printf("comment %s: rendering body: %v", span.comment.ID, err)
			bodyHTML = template.HTML(template.HTMLEscapeString(span.comment.Body))
		}
		renderedComments = append(renderedComments, renderedComment{
			ID:       span.comment.ID,
			Body:     bodyHTML,
			Orphaned: span.orphaned,
		})
	}

	// Re-sort rendered comments by startIndex ascending so the template emits
	// them in reading order (the JS re-positions them anyway, but this keeps the
	// DOM order sensible).
	sort.Slice(renderedComments, func(i, j int) bool {
		// spans is still sorted descending; find the original index.
		return findStartIndex(spans, renderedComments[i].ID) < findStartIndex(spans, renderedComments[j].ID)
	})

	return rendered, renderedComments, bootstrapped, nil
}

// sourceMatchesAt reports whether source contains text starting at position.
// Returns false if position is out of range or the text does not fit.
func sourceMatchesAt(source string, position int, text string) bool {
	if position < 0 || position+len(text) > len(source) {
		return false
	}
	return source[position:position+len(text)] == text
}

// findStartIndex returns the startIndex for the comment with the given ID from
// the spans slice. Returns 0 if not found (should not happen in practice).
func findStartIndex(spans []anchorSpan, id string) int {
	for _, span := range spans {
		if span.comment.ID == id {
			return span.startIndex
		}
	}
	return 0
}

// insertBytes inserts insertion into data at byte offset position, returning
// the new slice. The original slice is not modified.
func insertBytes(data []byte, position int, insertion []byte) []byte {
	result := make([]byte, len(data)+len(insertion))
	copy(result, data[:position])
	copy(result[position:], insertion)
	copy(result[position+len(insertion):], data[position:])
	return result
}

// countOverlappingOccurrences counts how many times sub appears in s, including
// overlapping occurrences. strings.Count only counts non-overlapping matches,
// so "aa" in "aaa" returns 1 there but 2 here.
func countOverlappingOccurrences(s, sub string) int {
	if sub == "" {
		return 0
	}
	count := 0
	start := 0
	for {
		index := strings.Index(s[start:], sub)
		if index == -1 {
			break
		}
		count++
		start += index + 1
	}
	return count
}

// fencedCodeBlockLineOffsets returns a set of byte offsets (one per line) that
// fall inside a fenced code block in the markdown source. A simple state machine
// is used: lines starting with ``` or ~~~ toggle the fence state. This matches
// what goldmark sees without attempting full markdown parsing.
func fencedCodeBlockLineOffsets(source string) map[int]bool {
	offsets := map[int]bool{}
	inFence := false
	lineOffset := 0
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		isFenceMarker := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		if isFenceMarker {
			inFence = !inFence
		} else if inFence {
			offsets[lineOffset] = true
		}
		// +1 for the newline that Split consumed.
		lineOffset += len(line) + 1
	}
	return offsets
}

// lineStartOffset returns the byte offset of the start of the line that
// contains the byte at position within source.
func lineStartOffset(source string, position int) int {
	lastNewline := strings.LastIndex(source[:position], "\n")
	if lastNewline == -1 {
		return 0
	}
	return lastNewline + 1
}
