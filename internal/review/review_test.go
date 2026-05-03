// SPDX-License-Identifier: AGPL-3.0-or-later

package review_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/skorokithakis/graphe/internal/review"
)

// source is a representative post body used across tests.
const source = "The quick brown fox jumps over the lazy dog. The fox is clever."

// redirectCacheDir points the review package's cache dir at a fresh temp
// directory for the duration of the test. The markdown file must also exist on
// disk because SidecarPath calls filepath.EvalSymlinks.
func redirectCacheDir(t *testing.T) string {
	t.Helper()
	cacheDir := t.TempDir()
	restore := review.SetUserCacheDirForTest(func() (string, error) {
		return cacheDir, nil
	})
	t.Cleanup(restore)
	return cacheDir
}

// loadEmpty creates a Store backed by a temp directory with no pre-existing sidecar.
// The markdown file is created on disk so that filepath.EvalSymlinks succeeds.
func loadEmpty(t *testing.T) *review.Store {
	t.Helper()
	redirectCacheDir(t)
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "post.md")
	// The file must exist for EvalSymlinks to resolve it.
	if err := os.WriteFile(mdPath, []byte(""), 0o600); err != nil {
		t.Fatalf("creating markdown file: %v", err)
	}
	store, err := review.Load(mdPath)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	return store
}

func TestLoad_MissingSidecarReturnsEmptyStore(t *testing.T) {
	store := loadEmpty(t)
	if comments := store.List(); len(comments) != 0 {
		t.Errorf("expected empty store, got %d comments", len(comments))
	}
}

func TestAdd_Success(t *testing.T) {
	store := loadEmpty(t)

	comment, err := store.Add("quick brown fox", "lazy dog", "Nice sentence.", source)
	if err != nil {
		t.Fatalf("Add returned unexpected error: %v", err)
	}

	if comment.ID == "" {
		t.Error("comment ID is empty")
	}
	if comment.Start != "quick brown fox" {
		t.Errorf("Start = %q, want %q", comment.Start, "quick brown fox")
	}
	if comment.End != "lazy dog" {
		t.Errorf("End = %q, want %q", comment.End, "lazy dog")
	}
	if comment.Body != "Nice sentence." {
		t.Errorf("Body = %q, want %q", comment.Body, "Nice sentence.")
	}
	if comment.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	comments := store.List()
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
}

func TestAdd_PersistsToDisk(t *testing.T) {
	redirectCacheDir(t)
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "post.md")
	if err := os.WriteFile(mdPath, []byte(""), 0o600); err != nil {
		t.Fatalf("creating markdown file: %v", err)
	}

	store, err := review.Load(mdPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err := store.Add("quick brown fox", "lazy dog", "body", source); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Reload from disk and verify the comment survived.
	store2, err := review.Load(mdPath)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if comments := store2.List(); len(comments) != 1 {
		t.Errorf("expected 1 comment after reload, got %d", len(comments))
	}
}

func TestAdd_NonUniqueStartAnchorReturnsError(t *testing.T) {
	store := loadEmpty(t)

	// "fox" appears twice in source.
	_, err := store.Add("fox", "lazy dog", "body", source)
	if err == nil {
		t.Fatal("expected error for non-unique start anchor, got nil")
	}

	// Store must remain empty — no partial write.
	if comments := store.List(); len(comments) != 0 {
		t.Errorf("expected 0 comments after failed Add, got %d", len(comments))
	}
}

func TestAdd_EndBeforeStartReturnsError(t *testing.T) {
	store := loadEmpty(t)

	// "lazy dog" appears before "clever" in source, so using "clever" as start
	// and "lazy dog" as end should trigger the end-before-start error.
	_, err := store.Add("clever", "lazy dog", "body", source)
	if err == nil {
		t.Fatal("expected error for end-before-start, got nil")
	}

	if comments := store.List(); len(comments) != 0 {
		t.Errorf("expected 0 comments after failed Add, got %d", len(comments))
	}
}

func TestEdit_BodyOnlySkipsAnchorRevalidation(t *testing.T) {
	store := loadEmpty(t)

	comment, err := store.Add("quick brown fox", "lazy dog", "original body", source)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Pass a source that would fail anchor validation to prove revalidation is skipped.
	badSource := "completely different text"
	newBody := "updated body"
	updated, err := store.Edit(comment.ID, nil, nil, &newBody, badSource)
	if err != nil {
		t.Fatalf("Edit (body-only) returned unexpected error: %v", err)
	}

	if updated.Body != "updated body" {
		t.Errorf("Body = %q, want %q", updated.Body, "updated body")
	}
	// Anchors must be unchanged.
	if updated.Start != "quick brown fox" {
		t.Errorf("Start changed unexpectedly: %q", updated.Start)
	}
}

func TestEdit_AnchorChangeTriggersRevalidation(t *testing.T) {
	store := loadEmpty(t)

	comment, err := store.Add("quick brown fox", "lazy dog", "body", source)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// "fox" appears twice, so changing start to "fox" must fail.
	newStart := "fox"
	_, err = store.Edit(comment.ID, &newStart, nil, nil, source)
	if err == nil {
		t.Fatal("expected error when changing start to non-unique anchor, got nil")
	}

	// Original comment must be unchanged.
	comments := store.List()
	if len(comments) != 1 || comments[0].Start != "quick brown fox" {
		t.Errorf("comment was mutated after failed Edit: %+v", comments)
	}
}

func TestDelete_RemovesComment(t *testing.T) {
	store := loadEmpty(t)

	comment, err := store.Add("quick brown fox", "lazy dog", "body", source)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := store.Delete(comment.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if comments := store.List(); len(comments) != 0 {
		t.Errorf("expected 0 comments after Delete, got %d", len(comments))
	}
}

func TestDelete_UnknownIDReturnsError(t *testing.T) {
	store := loadEmpty(t)

	if err := store.Delete("c-xxxx"); err == nil {
		t.Fatal("expected error deleting unknown ID, got nil")
	}
}

func TestAdd_EndInsideStartReturnsError(t *testing.T) {
	store := loadEmpty(t)

	// "quick" falls inside "quick brown fox", so end is inside start.
	_, err := store.Add("quick brown fox", "quick", "body", source)
	if err == nil {
		t.Fatal("expected error when end anchor falls inside start anchor, got nil")
	}
}

func TestAdd_OverlappingAnchorCountedCorrectly(t *testing.T) {
	store := loadEmpty(t)

	// "aa" appears twice in "aaa" (overlapping), so it must be rejected as
	// ambiguous even though strings.Count would return 1.
	overlappingSource := "aaa bbb ccc"
	_, err := store.Add("aa", "bbb", "body", overlappingSource)
	if err == nil {
		t.Fatal("expected error for overlapping start anchor, got nil")
	}
}

func TestClear_RemovesAllCommentsAndDeletesSidecar(t *testing.T) {
	redirectCacheDir(t)
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "post.md")
	if err := os.WriteFile(mdPath, []byte(""), 0o600); err != nil {
		t.Fatalf("creating markdown file: %v", err)
	}

	store, err := review.Load(mdPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err := store.Add("quick brown fox", "lazy dog", "body one", source); err != nil {
		t.Fatalf("Add first: %v", err)
	}
	if _, err := store.Add("jumps over", "clever.", "body two", source); err != nil {
		t.Fatalf("Add second: %v", err)
	}

	count, err := store.Clear()
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if count != 2 {
		t.Errorf("Clear returned count %d, want 2", count)
	}

	// In-memory slice must be empty.
	if comments := store.List(); len(comments) != 0 {
		t.Errorf("expected 0 comments after Clear, got %d", len(comments))
	}

	// Sidecar file must be gone from disk.
	sidecarPath, err := review.SidecarPath(mdPath)
	if err != nil {
		t.Fatalf("SidecarPath: %v", err)
	}
	if _, err := os.Stat(sidecarPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sidecar file still exists after Clear: %v", err)
	}
}

func TestClear_IdempotentWhenNoSidecar(t *testing.T) {
	store := loadEmpty(t)

	// First clear on a store with no sidecar must succeed and return 0.
	count, err := store.Clear()
	if err != nil {
		t.Fatalf("Clear on empty store: %v", err)
	}
	if count != 0 {
		t.Errorf("Clear returned count %d, want 0", count)
	}

	// Second clear must also succeed (idempotent).
	count, err = store.Clear()
	if err != nil {
		t.Fatalf("second Clear: %v", err)
	}
	if count != 0 {
		t.Errorf("second Clear returned count %d, want 0", count)
	}
}

func TestClear_SidecarDeletedExternallyReturnsZero(t *testing.T) {
	redirectCacheDir(t)
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "post.md")
	if err := os.WriteFile(mdPath, []byte(""), 0o600); err != nil {
		t.Fatalf("creating markdown file: %v", err)
	}

	store, err := review.Load(mdPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err := store.Add("quick brown fox", "lazy dog", "body", source); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Simulate an external delete (or a race between Load and Clear) by removing
	// the sidecar before Clear is called.
	sidecarPath, err := review.SidecarPath(mdPath)
	if err != nil {
		t.Fatalf("SidecarPath: %v", err)
	}
	if err := os.Remove(sidecarPath); err != nil {
		t.Fatalf("os.Remove sidecar: %v", err)
	}

	count, err := store.Clear()
	if err != nil {
		t.Fatalf("Clear after external delete: %v", err)
	}
	if count != 0 {
		t.Errorf("Clear returned count %d, want 0 when sidecar was already gone", count)
	}

	// In-memory slice must still be cleared.
	if comments := store.List(); len(comments) != 0 {
		t.Errorf("expected 0 comments after Clear, got %d", len(comments))
	}
}

func TestSidecarPath_InCacheDir(t *testing.T) {
	cacheDir := redirectCacheDir(t)
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "my-post.md")
	if err := os.WriteFile(mdPath, []byte(""), 0o600); err != nil {
		t.Fatalf("creating markdown file: %v", err)
	}

	store, err := review.Load(mdPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err := store.Add("quick brown fox", "lazy dog", "body", source); err != nil {
		t.Fatalf("Add: %v", err)
	}

	sidecarPath, err := review.SidecarPath(mdPath)
	if err != nil {
		t.Fatalf("SidecarPath: %v", err)
	}

	// Sidecar must live inside the cache dir, not next to the markdown file.
	if _, err := os.Stat(sidecarPath); err != nil {
		t.Errorf("sidecar file not found at cache path %s: %v", sidecarPath, err)
	}
	if filepath.Dir(sidecarPath) != filepath.Join(cacheDir, "graphe") {
		t.Errorf("sidecar not in cache dir: got %s, want dir %s", sidecarPath, filepath.Join(cacheDir, "graphe"))
	}
}

func TestSidecarPath_DifferentDirsSameBasename(t *testing.T) {
	redirectCacheDir(t)

	dir1 := t.TempDir()
	dir2 := t.TempDir()
	mdPath1 := filepath.Join(dir1, "post.md")
	mdPath2 := filepath.Join(dir2, "post.md")
	if err := os.WriteFile(mdPath1, []byte(""), 0o600); err != nil {
		t.Fatalf("creating markdown file 1: %v", err)
	}
	if err := os.WriteFile(mdPath2, []byte(""), 0o600); err != nil {
		t.Fatalf("creating markdown file 2: %v", err)
	}

	sidecar1, err := review.SidecarPath(mdPath1)
	if err != nil {
		t.Fatalf("SidecarPath 1: %v", err)
	}
	sidecar2, err := review.SidecarPath(mdPath2)
	if err != nil {
		t.Fatalf("SidecarPath 2: %v", err)
	}

	// Two files with the same basename in different directories must get
	// different sidecars.
	if sidecar1 == sidecar2 {
		t.Errorf("same sidecar path for different directories: %s", sidecar1)
	}
}
