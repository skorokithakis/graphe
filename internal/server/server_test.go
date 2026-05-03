// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skorokithakis/graphe/internal/review"
	"github.com/skorokithakis/graphe/internal/server"
)

// fixtureMarkdown is a minimal post with a known sentence that the test comment
// will anchor to.
const fixtureMarkdown = `# Test Post

The quick brown fox jumps over the lazy dog.

Another paragraph here.
`

// fixtureSidecar is the JSON sidecar for the fixture post. The comment anchors
// "quick brown fox" -> "lazy dog", which is entirely within one paragraph so
// the resulting HTML should be well-formed.
var fixtureSidecar = map[string]interface{}{
	"comments": []map[string]interface{}{
		{
			"id":         "c-test",
			"start":      "quick brown fox",
			"end":        "lazy dog",
			"body":       "Classic pangram.",
			"created_at": time.Now().UTC().Format(time.RFC3339),
		},
	},
}

// redirectCacheDir points the review package's cache dir at a fresh temp
// directory for the duration of the test.
func redirectCacheDir(t *testing.T) string {
	t.Helper()
	cacheDir := t.TempDir()
	restore := review.SetUserCacheDirForTest(func() (string, error) {
		return cacheDir, nil
	})
	t.Cleanup(restore)
	return cacheDir
}

// writeFixtures writes the markdown fixture and its sidecar to a temp dir,
// placing the sidecar in the (redirected) cache dir. Returns the markdown path.
func writeFixtures(t *testing.T) string {
	t.Helper()
	redirectCacheDir(t)
	dir := t.TempDir()

	mdPath := filepath.Join(dir, "post.md")
	if err := os.WriteFile(mdPath, []byte(fixtureMarkdown), 0o600); err != nil {
		t.Fatalf("writing markdown fixture: %v", err)
	}

	// Derive the sidecar path from the review package so it lands in the cache dir.
	sidecarPath, err := review.SidecarPath(mdPath)
	if err != nil {
		t.Fatalf("resolving sidecar path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sidecarPath), 0o700); err != nil {
		t.Fatalf("creating cache dir: %v", err)
	}
	sidecarData, err := json.MarshalIndent(fixtureSidecar, "", "  ")
	if err != nil {
		t.Fatalf("marshalling sidecar fixture: %v", err)
	}
	if err := os.WriteFile(sidecarPath, sidecarData, 0o600); err != nil {
		t.Fatalf("writing sidecar fixture: %v", err)
	}

	return mdPath
}

func TestServer_AnchorMarkPresent(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / returned %d, want 200", recorder.Code)
	}

	body := recorder.Body.String()

	// The anchor marker must appear in the rendered HTML.
	if !strings.Contains(body, `<mark class="anchor"`) {
		t.Errorf("rendered HTML does not contain <mark class=\"anchor\"; got:\n%s", body)
	}

	// The specific comment ID must be present in the mark tag.
	if !strings.Contains(body, `data-comment-id="c-test"`) {
		t.Errorf("rendered HTML does not contain data-comment-id=\"c-test\"; got:\n%s", body)
	}

	// The anchor text itself must appear inside the mark.
	if !strings.Contains(body, "quick brown fox") {
		t.Errorf("rendered HTML does not contain anchor text; got:\n%s", body)
	}
}

func TestServer_ReloadUpdatesContent(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Overwrite the markdown with new content.
	newContent := "# Updated Post\n\nFresh content.\n"
	if err := os.WriteFile(mdPath, []byte(newContent), 0o600); err != nil {
		t.Fatalf("overwriting markdown: %v", err)
	}

	if err := srv.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if !strings.Contains(body, "Updated Post") {
		t.Errorf("page does not reflect reloaded content; got:\n%s", body)
	}
}

func TestServer_NotFoundForUnknownPath(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	srv.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("GET /nonexistent returned %d, want 404", recorder.Code)
	}
}

// TestServer_DeleteComment_Success verifies that a DELETE request for an
// existing comment returns 204 and removes the comment from the sidecar.
func TestServer_DeleteComment_Success(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/comments/c-test", nil)
	srv.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("DELETE /comments/c-test returned %d, want 204", recorder.Code)
	}

	// Confirm the sidecar no longer contains the comment.
	sidecarPath, err := review.SidecarPath(mdPath)
	if err != nil {
		t.Fatalf("resolving sidecar path: %v", err)
	}
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("reading sidecar after delete: %v", err)
	}
	if strings.Contains(string(data), "c-test") {
		t.Errorf("sidecar still contains c-test after delete; sidecar:\n%s", data)
	}
}

// TestServer_DeleteComment_NotFound verifies that a DELETE request for an
// unknown comment id returns 404.
func TestServer_DeleteComment_NotFound(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/comments/c-zzzz", nil)
	srv.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("DELETE /comments/c-zzzz returned %d, want 404", recorder.Code)
	}
}

// TestServer_DeleteComment_MalformedID verifies that a DELETE request with an
// id that does not match the c-xxxx format returns 400 without touching disk.
func TestServer_DeleteComment_MalformedID(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	malformedIDs := []string{
		"c-",
		"c-toolong",
		"notanid",
		"c-TEST",
	}

	for _, id := range malformedIDs {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodDelete, "/comments/"+id, nil)
		srv.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("DELETE /comments/%s returned %d, want 400", id, recorder.Code)
		}
	}
}

// TestServer_OrphanedAfterAnchorEdit verifies that editing the anchor text of a
// comment causes it to render as an orphan pin rather than disappearing. This
// is the primary acceptance criterion for the diff-based relocation feature.
func TestServer_OrphanedAfterAnchorEdit(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Confirm the comment is anchored on first load.
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(recorder.Body.String(), `<mark class="anchor"`) {
		t.Fatalf("initial load: expected anchored mark, got:\n%s", recorder.Body.String())
	}

	// Overwrite the markdown so the anchor text no longer exists.
	edited := strings.ReplaceAll(fixtureMarkdown, "quick brown fox", "speedy orange fox")
	if err := os.WriteFile(mdPath, []byte(edited), 0o600); err != nil {
		t.Fatalf("writing edited markdown: %v", err)
	}

	if err := srv.Reload(); err != nil {
		t.Fatalf("Reload after edit: %v", err)
	}

	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()

	// The anchor mark must be gone (the text was edited).
	if strings.Contains(body, `<mark class="anchor"`) {
		t.Errorf("after anchor edit: expected no <mark class=\"anchor\", but found one; body:\n%s", body)
	}

	// An orphan pin must appear in its place.
	if !strings.Contains(body, `class="orphan-pin"`) {
		t.Errorf("after anchor edit: expected orphan-pin span, got:\n%s", body)
	}
	if !strings.Contains(body, `data-comment-id="c-test"`) {
		t.Errorf("after anchor edit: expected data-comment-id=\"c-test\" on orphan pin, got:\n%s", body)
	}
}

// TestServer_NoJumpOnDuplicateAnchorInsert verifies that inserting a new
// paragraph containing the same anchor text earlier in the document does not
// cause the existing comment to jump to the new occurrence. The comment must
// stay at its original position (tracked via byte offsets) and remain anchored.
func TestServer_NoJumpOnDuplicateAnchorInsert(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Confirm the comment is anchored on first load.
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(recorder.Body.String(), `<mark class="anchor"`) {
		t.Fatalf("initial load: expected anchored mark, got:\n%s", recorder.Body.String())
	}

	// Prepend a new paragraph that contains the same anchor text. After this
	// edit the anchor text appears twice, so strict lookup would fail. The
	// diff-based tracker must keep the comment at its original (now second)
	// occurrence.
	withDuplicate := "# Test Post\n\nThe quick brown fox is mentioned here too.\n\n" +
		"The quick brown fox jumps over the lazy dog.\n\nAnother paragraph here.\n"
	if err := os.WriteFile(mdPath, []byte(withDuplicate), 0o600); err != nil {
		t.Fatalf("writing markdown with duplicate anchor: %v", err)
	}

	if err := srv.Reload(); err != nil {
		t.Fatalf("Reload after duplicate insert: %v", err)
	}

	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()

	// The comment must still be anchored (not orphaned, not missing).
	if !strings.Contains(body, `<mark class="anchor"`) {
		t.Errorf("after duplicate insert: expected anchored mark, got:\n%s", body)
	}
	if strings.Contains(body, `class="orphan-pin"`) {
		t.Errorf("after duplicate insert: comment should not be orphaned, got:\n%s", body)
	}

	// The mark must appear only once (the comment must not have jumped to the
	// first occurrence).
	markCount := strings.Count(body, `<mark class="anchor" data-comment-id="c-test">`)
	if markCount != 1 {
		t.Errorf("after duplicate insert: expected exactly 1 mark for c-test, got %d; body:\n%s", markCount, body)
	}
}

// TestServer_OrphanOverlapDoesNotBlockLaterAnchoredComment is a regression test
// for the overlap filter treating orphan spans as zero-width. Previously the
// filter used the orphan's stale endIndex to compute its extent, which caused a
// later, perfectly valid anchored comment whose startIndex fell between the
// orphan's translated startIndex and endIndex to be incorrectly dropped.
//
// Setup: comment A anchors "ALPHA...OMEGA". Comment B anchors "ZETA", which
// appears after OMEGA so both render fine on the initial load. The markdown is
// then edited to delete " OMEGA" (the text between ALPHA and ZETA). After the
// edit, A's end anchor is gone so A becomes orphaned; A's translated endIndex
// collapses to the deletion point. B's translated startIndex also shifts left
// to the same deletion point, landing between A's translated startIndex and
// A's translated endIndex + len(A.End). The old code used that stale extent
// and dropped B; the fix treats orphan spans as zero-width so B is kept.
func TestServer_OrphanOverlapDoesNotBlockLaterAnchoredComment(t *testing.T) {
	redirectCacheDir(t)
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "post.md")

	// "ALPHA OMEGA ZETA." — A spans ALPHA→OMEGA, B anchors ZETA (after OMEGA).
	initialMarkdown := "# Post\n\nALPHA OMEGA ZETA.\n"
	if err := os.WriteFile(mdPath, []byte(initialMarkdown), 0o600); err != nil {
		t.Fatalf("writing initial markdown: %v", err)
	}

	sidecar := map[string]interface{}{
		"comments": []map[string]interface{}{
			{
				"id":         "c-aaaa",
				"start":      "ALPHA",
				"end":        "OMEGA",
				"body":       "Wide comment A.",
				"created_at": time.Now().UTC().Format(time.RFC3339),
			},
			{
				"id":         "c-bbbb",
				"start":      "ZETA",
				"end":        "ZETA",
				"body":       "Narrow comment B.",
				"created_at": time.Now().UTC().Add(time.Second).Format(time.RFC3339),
			},
		},
	}

	sidecarPath, err := review.SidecarPath(mdPath)
	if err != nil {
		t.Fatalf("resolving sidecar path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sidecarPath), 0o700); err != nil {
		t.Fatalf("creating cache dir: %v", err)
	}
	sidecarData, err := json.MarshalIndent(sidecar, "", "  ")
	if err != nil {
		t.Fatalf("marshalling sidecar: %v", err)
	}
	if err := os.WriteFile(sidecarPath, sidecarData, 0o600); err != nil {
		t.Fatalf("writing sidecar: %v", err)
	}

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Confirm both comments are anchored on first load. A's span ends before
	// ZETA so the overlap filter must not drop B.
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `data-comment-id="c-aaaa"`) {
		t.Fatalf("initial load: c-aaaa not found; body:\n%s", body)
	}
	if !strings.Contains(body, `data-comment-id="c-bbbb"`) {
		t.Fatalf("initial load: c-bbbb not found; body:\n%s", body)
	}

	// Delete " OMEGA" from the source. A's end anchor disappears; ZETA remains.
	// After diff translation, A's endIndex collapses to the deletion point and
	// B's startIndex shifts left to the same point. B's startIndex now falls
	// between A's startIndex and A's endIndex + len("OMEGA"), which is the
	// condition that triggered the bug.
	editedMarkdown := "# Post\n\nALPHA ZETA.\n"
	if err := os.WriteFile(mdPath, []byte(editedMarkdown), 0o600); err != nil {
		t.Fatalf("writing edited markdown: %v", err)
	}

	if err := srv.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	recorder = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	body = recorder.Body.String()

	// A must be orphaned (its end anchor was deleted).
	if !strings.Contains(body, `class="orphan-pin" data-comment-id="c-aaaa"`) {
		t.Errorf("after edit: expected c-aaaa to be an orphan-pin; body:\n%s", body)
	}

	// B must still be rendered as an anchored <mark>, not dropped by the overlap filter.
	if !strings.Contains(body, `<mark class="anchor" data-comment-id="c-bbbb">`) {
		t.Errorf("after edit: expected c-bbbb to be an anchored mark, but it was dropped; body:\n%s", body)
	}
}

// TestServer_WatchBroadcastsReloadOnFileChange verifies that modifying the
// markdown file causes Watch to call Reload and broadcast a "reload" SSE event
// to subscribers within a reasonable time window.
func TestServer_WatchBroadcastsReloadOnFileChange(t *testing.T) {
	mdPath := writeFixtures(t)

	srv, err := server.New(mdPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Subscribe to SSE events before starting the watcher so we don't miss the
	// broadcast.
	events := srv.SubscribeForTest()
	defer srv.UnsubscribeForTest(events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchDone := make(chan error, 1)
	go func() {
		watchDone <- srv.Watch(ctx)
	}()

	// Give the watcher goroutine time to register the inotify watch before we
	// write the file. Without this, the write can race the Add() call.
	time.Sleep(50 * time.Millisecond)

	// Overwrite the markdown file to trigger a change event.
	newContent := "# Watched Post\n\nChanged content.\n"
	if err := os.WriteFile(mdPath, []byte(newContent), 0o600); err != nil {
		t.Fatalf("writing updated markdown: %v", err)
	}

	// Expect a "reload" event within 500ms (debounce is 150ms, plus OS latency).
	select {
	case event := <-events:
		if event != "reload" {
			t.Errorf("expected event %q, got %q", "reload", event)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for reload event")
	}

	// Verify the server actually reloaded the new content.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), "Watched Post") {
		t.Errorf("page does not reflect reloaded content after Watch triggered Reload")
	}

	// Cancel the context and confirm Watch exits cleanly.
	cancel()
	select {
	case err := <-watchDone:
		if err != nil {
			t.Errorf("Watch returned error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Watch did not exit after context cancellation")
	}
}
