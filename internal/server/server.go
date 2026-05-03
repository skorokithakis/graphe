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
	"sort"
	"strings"
	"sync"

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
	ID   string
	Body template.HTML
}

// renderedPage is the data passed to pageTemplate.
type renderedPage struct {
	Title    string
	HTML     template.HTML
	CSS      template.CSS
	JS       template.JS
	Comments []renderedComment
}

// Server loads a markdown post and its review comments and serves them over HTTP.
type Server struct {
	mdPath string

	// mu guards rendered; Reload writes it, HTTP handlers read it.
	mu       sync.RWMutex
	rendered renderedPage

	// broadcaster distributes SSE events to connected clients.
	// The live-reload ticket (gra-dmtfx) will send real events through this;
	// for now the SSE handler only uses it for the heartbeat loop.
	broadcaster *broadcaster
}

// New creates a Server for the markdown file at mdPath and performs the initial
// load. Returns an error if the file cannot be read or rendered.
func New(mdPath string) (*Server, error) {
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

	// Build the annotated HTML: insert <mark> elements into the markdown source
	// at the byte offsets of each comment's anchor span, then render.
	annotatedHTML, renderedComments, err := buildAnnotatedPage(loadedPost, comments)
	if err != nil {
		return fmt.Errorf("building annotated page: %w", err)
	}

	page := renderedPage{
		Title:    loadedPost.Title,
		HTML:     annotatedHTML,
		CSS:      template.CSS(cssContent),
		JS:       template.JS(jsContent),
		Comments: renderedComments,
	}

	s.mu.Lock()
	s.rendered = page
	s.mu.Unlock()

	return nil
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

// Handler returns an http.Handler that serves the post and SSE endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /events", s.handleEvents)
	return mux
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
}

// buildAnnotatedPage inserts <mark> elements into the markdown source at each
// comment's anchor span, renders the result, and returns the HTML along with
// the rendered comment bodies.
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
func buildAnnotatedPage(loadedPost *post.Post, comments []review.Comment) (template.HTML, []renderedComment, error) {
	source := loadedPost.Source

	// Resolve byte offsets for each comment, skipping any that are ambiguous or
	// missing (which can happen if the post was edited after the comment was added).
	var spans []anchorSpan
	for _, comment := range comments {
		startIndex := strings.Index(source, comment.Start)
		if startIndex == -1 {
			log.Printf("comment %s: start anchor %q not found in source, skipping", comment.ID, comment.Start)
			continue
		}
		if strings.Count(source, comment.Start) > 1 {
			log.Printf("comment %s: start anchor %q is ambiguous, skipping", comment.ID, comment.Start)
			continue
		}

		endIndex := strings.Index(source, comment.End)
		if endIndex == -1 {
			log.Printf("comment %s: end anchor %q not found in source, skipping", comment.ID, comment.End)
			continue
		}
		if strings.Count(source, comment.End) > 1 {
			log.Printf("comment %s: end anchor %q is ambiguous, skipping", comment.ID, comment.End)
			continue
		}

		spans = append(spans, anchorSpan{
			comment:    comment,
			startIndex: startIndex,
			endIndex:   endIndex,
		})
	}

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
		endOfSpan := span.endIndex + len(span.comment.End)
		closeTag := []byte("</mark>")
		openTag := []byte(fmt.Sprintf(`<mark class="anchor" data-comment-id="%s">`, span.comment.ID))

		// Insert closing tag first (higher offset) so the opening tag insertion
		// does not shift the closing tag's position.
		annotated = insertBytes(annotated, endOfSpan, closeTag)
		annotated = insertBytes(annotated, span.startIndex, openTag)
	}

	rendered, err := post.Render(string(annotated))
	if err != nil {
		return "", nil, fmt.Errorf("rendering annotated markdown: %w", err)
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
			ID:   span.comment.ID,
			Body: bodyHTML,
		})
	}

	// Re-sort rendered comments by startIndex ascending so the template emits
	// them in reading order (the JS re-positions them anyway, but this keeps the
	// DOM order sensible).
	sort.Slice(renderedComments, func(i, j int) bool {
		// spans is still sorted descending; find the original index.
		return findStartIndex(spans, renderedComments[i].ID) < findStartIndex(spans, renderedComments[j].ID)
	})

	return rendered, renderedComments, nil
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
