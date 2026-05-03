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
	DocID    string
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

	docID, err := review.DocID(s.mdPath)
	if err != nil {
		return fmt.Errorf("deriving doc id: %w", err)
	}

	page := renderedPage{
		Title:    loadedPost.Title,
		DocID:    docID,
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
		// Catch-all GET handler: any path that did not match a more specific
		// route (e.g. /events, /comments/{id}) lands here. Redirect to / so
		// stray links and typos always end up on the post rather than a 404.
		// Only GETs reach this branch because the route is registered as
		// "GET /"; non-GET methods to unknown paths still get the mux's
		// default 404/405. The page itself inlines its CSS/JS and references
		// no external assets, so this redirect cannot loop on its own.
		http.Redirect(writer, request, "/", http.StatusFound)
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

	// Build a map from line-start byte offset to the opening-fence line offset
	// for every line that must not receive an inserted <mark> open tag (fence
	// marker lines and lines inside the fence body). The opening-fence offset is
	// used to place orphan pins just before the fence, where goldmark renders
	// them as real HTML rather than escaping them inside the <pre>.
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

			// If the translated position now falls inside a fenced code block (or
			// on a fence-marker line), inserting a <mark> would break goldmark's
			// fence detection or be escaped inside the <pre>. Downgrade to an
			// orphan and move the pin to the opening fence line, which is outside
			// the fence body and renders as real HTML.
			if anchored {
				if openingFence, inFence := fencedLineOffsets[lineStartOffset(source, startIndex)]; inFence {
					anchored = false
					startIndex = openingFence
				}
			}

			// Shift the open-tag past any block-level prefix when the anchor
			// starts at the very beginning of a line. Without this, goldmark sees
			// the raw <mark> before the '#'/'- '/'> ' marker and renders the line
			// as an inline-HTML paragraph instead of a heading/list/blockquote.
			// The shift is only applied to anchored spans; orphan pins are
			// zero-width <span> elements that do not affect block parsing.
			if anchored {
				lineStart := lineStartOffset(source, startIndex)
				if startIndex == lineStart {
					shifted := skipBlockPrefixes(source, lineStart)
					endOfSpan := endIndex + len(comment.End)
					if shifted < endOfSpan {
						startIndex = shifted
					}
				}
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

		if openingFence, inFence := fencedLineOffsets[lineStartOffset(source, startIndex)]; inFence {
			// The anchor lands on a fence-marker line or inside a fenced code
			// block. Inserting a <mark> there would break goldmark's fence
			// detection or be escaped inside the <pre>. Downgrade to an orphan
			// pin placed at the opening fence line, which is outside the fence
			// body and renders as real HTML. The raw startIndex/endIndex are
			// stored so the diff tracker can follow the anchor through future
			// edits; the pin's visual position uses openingFence.
			bootstrapped[comment.ID] = commentOffset{start: startIndex, end: endIndex}
			spans = append(spans, anchorSpan{
				comment:    comment,
				startIndex: openingFence,
				endIndex:   endIndex,
				orphaned:   true,
			})
			continue
		}

		// Shift the open-tag past any block-level prefix when the anchor
		// starts at the very beginning of a line. Same rationale as the
		// known-offset path above.
		lineStart := lineStartOffset(source, startIndex)
		if startIndex == lineStart {
			shifted := skipBlockPrefixes(source, lineStart)
			endOfSpan := endIndex + len(comment.End)
			if shifted < endOfSpan {
				startIndex = shifted
			}
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
			// When the pin lands at the very start of a line (e.g. it was
			// relocated to an opening fence marker line), goldmark sees the
			// inline HTML and the fence marker on the same line:
			//   <span ...></span>```python
			// and parses the whole line as an inline-HTML paragraph, breaking
			// the fence. A trailing newline pushes the fence marker to its own
			// line so goldmark recognises it correctly. Mid-line pins must not
			// get the newline — it would split a paragraph mid-sentence.
			if span.startIndex == lineStartOffset(source, span.startIndex) {
				pin = append(pin, '\n')
			}
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

// fencedCodeBlockLineOffsets returns a map from line-start byte offset to the
// byte offset of the corresponding opening fence marker line, for every line
// that must not receive an inserted <mark> open tag. A simple state machine is
// used: lines starting with ``` or ~~~ toggle the fence state. This matches
// what goldmark sees without attempting full markdown parsing.
//
// Both fence-marker lines and lines inside the fence body are included. An
// anchor starting on a fence-marker line would open the <mark> before the
// backticks, causing goldmark to parse the line as an inline-HTML paragraph and
// break the code block entirely. The opening-fence offset stored as the value
// lets callers place orphan pins just before the fence, where goldmark renders
// them as real HTML rather than escaping them inside the <pre>.
func fencedCodeBlockLineOffsets(source string) map[int]int {
	// openingFence is the line offset of the most recently seen opening fence
	// marker. -1 means we are not currently inside a fence.
	openingFence := -1
	offsets := map[int]int{}
	lineOffset := 0
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		isFenceMarker := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		if isFenceMarker {
			if openingFence == -1 {
				// Opening fence: record the marker line itself. The opening
				// fence maps to itself so callers always get a valid insertion
				// point outside the fence body.
				openingFence = lineOffset
				offsets[lineOffset] = lineOffset
			} else {
				// Closing fence: record it and reset state.
				offsets[lineOffset] = openingFence
				openingFence = -1
			}
		} else if openingFence != -1 {
			offsets[lineOffset] = openingFence
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

// skipBlockPrefixes returns the offset past all stacked block-level prefixes on
// the line beginning at lineStart. It handles up to 3 leading spaces of
// indentation, then one marker per pass: ATX heading markers ('#{1,6}' +
// space/tab), bullet list markers ('[-+*]' + space/tab), ordered list markers
// ('\d{1,9}[.)]' + space/tab), and blockquote markers ('>' + optional space).
// Blockquote markers are repeated to handle nesting (e.g. '> ## ' skips both
// the '> ' and the '## '). The returned offset is always >= lineStart.
//
// Setext headings ('===' or '---' underlines) cannot be fixed by shifting the
// open-tag position because the heading text and the underline are on separate
// lines; the <mark> would still open before the underline. We leave them as-is.
func skipBlockPrefixes(source string, lineStart int) int {
	position := lineStart
	sourceLength := len(source)

	for {
		if position >= sourceLength {
			return position
		}

		// Skip up to 3 leading spaces (CommonMark allows 0–3 spaces before
		// block markers; 4+ spaces trigger an indented code block instead).
		spaceCount := 0
		for spaceCount < 3 && position+spaceCount < sourceLength && source[position+spaceCount] == ' ' {
			spaceCount++
		}
		afterSpaces := position + spaceCount

		if afterSpaces >= sourceLength {
			return position
		}

		advanced := false

		// ATX heading: '#{1,6}' followed by a space or tab.
		if source[afterSpaces] == '#' {
			hashCount := 0
			for hashCount < 6 && afterSpaces+hashCount < sourceLength && source[afterSpaces+hashCount] == '#' {
				hashCount++
			}
			afterHashes := afterSpaces + hashCount
			if hashCount >= 1 && afterHashes < sourceLength && (source[afterHashes] == ' ' || source[afterHashes] == '\t') {
				position = afterHashes + 1
				advanced = true
			}
		}

		// Bullet list: '[-+*]' followed by a space or tab.
		if !advanced && (source[afterSpaces] == '-' || source[afterSpaces] == '+' || source[afterSpaces] == '*') {
			afterMarker := afterSpaces + 1
			if afterMarker < sourceLength && (source[afterMarker] == ' ' || source[afterMarker] == '\t') {
				position = afterMarker + 1
				advanced = true
			}
		}

		// Ordered list: 1–9 digits followed by '.' or ')' and a space or tab.
		if !advanced && source[afterSpaces] >= '0' && source[afterSpaces] <= '9' {
			digitCount := 0
			for digitCount < 9 && afterSpaces+digitCount < sourceLength && source[afterSpaces+digitCount] >= '0' && source[afterSpaces+digitCount] <= '9' {
				digitCount++
			}
			afterDigits := afterSpaces + digitCount
			if digitCount >= 1 && afterDigits < sourceLength && (source[afterDigits] == '.' || source[afterDigits] == ')') {
				afterPunct := afterDigits + 1
				if afterPunct < sourceLength && (source[afterPunct] == ' ' || source[afterPunct] == '\t') {
					position = afterPunct + 1
					advanced = true
				}
			}
		}

		// Blockquote: '>' followed by an optional space. Repeated each pass to
		// handle nested blockquotes (e.g. '> > ' advances twice).
		if !advanced && source[afterSpaces] == '>' {
			afterMarker := afterSpaces + 1
			// Consume the optional space after '>'.
			if afterMarker < sourceLength && source[afterMarker] == ' ' {
				afterMarker++
			}
			position = afterMarker
			advanced = true
			// Loop again: the next pass may find another '>' or a heading/list
			// marker inside the blockquote.
			continue
		}

		if !advanced {
			return position
		}

		// A non-blockquote marker was consumed; stop after one such marker.
		return position
	}
}
