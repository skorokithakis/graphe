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

func writeFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	mdPath := filepath.Join(dir, "post.md")
	if err := os.WriteFile(mdPath, []byte(fixtureMarkdown), 0o600); err != nil {
		t.Fatalf("writing markdown fixture: %v", err)
	}

	sidecarPath := filepath.Join(dir, "post.graphe")
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
