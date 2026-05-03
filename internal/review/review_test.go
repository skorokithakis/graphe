// SPDX-License-Identifier: AGPL-3.0-or-later

package review_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/skorokithakis/graphe/internal/review"
)

// source is a representative post body used across tests.
const source = "The quick brown fox jumps over the lazy dog. The fox is clever."

// loadEmpty creates a Store backed by a temp directory with no pre-existing sidecar.
func loadEmpty(t *testing.T) *review.Store {
	t.Helper()
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "post.md")
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
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "post.md")

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

func TestSidecarPath_DerivedCorrectly(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "my-post.md")

	store, err := review.Load(mdPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err := store.Add("quick brown fox", "lazy dog", "body", source); err != nil {
		t.Fatalf("Add: %v", err)
	}

	expectedSidecar := filepath.Join(dir, "my-post-review.json")
	if _, err := os.Stat(expectedSidecar); err != nil {
		t.Errorf("sidecar file not found at expected path %s: %v", expectedSidecar, err)
	}
}
