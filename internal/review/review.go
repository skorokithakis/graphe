// SPDX-License-Identifier: AGPL-3.0-or-later

// Package review handles the comment store for a single markdown post.
// Each post's comments are persisted in a sidecar file inside the user's
// cache directory, keyed by a hash of the markdown file's absolute path.
package review

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// userCacheDir is a function variable so tests can redirect the cache to a
// temporary directory without touching $HOME or $XDG_CACHE_HOME. Production
// code always uses os.UserCacheDir. It is exported via SetUserCacheDirForTest
// in testing_helpers.go for use by external test packages.
var userCacheDir = os.UserCacheDir

// Comment is a single review annotation anchored to a substring of the post source.
type Comment struct {
	ID        string    `json:"id"`
	Start     string    `json:"start"`
	End       string    `json:"end"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Store holds all comments for one post and knows where to persist them.
type Store struct {
	// sidecarPath is the path to the JSON file inside the user cache dir.
	sidecarPath string
	comments    []Comment
}

// sidecarJSON is the on-disk shape of the JSON file.
type sidecarJSON struct {
	Comments []Comment `json:"comments"`
}

// Load reads the sidecar file for the given markdown path. If the sidecar does
// not exist, an empty Store is returned without error.
func Load(mdPath string) (*Store, error) {
	sidecarPath, err := SidecarPath(mdPath)
	if err != nil {
		return nil, fmt.Errorf("resolving sidecar path: %w", err)
	}

	data, err := os.ReadFile(sidecarPath)
	if errors.Is(err, os.ErrNotExist) {
		return &Store{sidecarPath: sidecarPath}, nil
	}
	if err != nil {
		return nil, err
	}

	var payload sidecarJSON
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", sidecarPath, err)
	}

	return &Store{
		sidecarPath: sidecarPath,
		comments:    payload.Comments,
	}, nil
}

// Save writes the store to its sidecar file. Comments are sorted by CreatedAt
// ascending so that diffs are stable regardless of insertion order.
//
// The cache directory is created here on first write rather than at Load time
// so that reading a post with no comments never creates an empty directory.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.sidecarPath), 0o700); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	sorted := make([]Comment, len(s.comments))
	copy(sorted, s.comments)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
	})

	payload := sidecarJSON{Comments: sorted}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	// Trailing newline for clean diffs.
	data = append(data, '\n')

	return os.WriteFile(s.sidecarPath, data, 0o600)
}

// Add creates a new comment anchored to [start, end) within source and appends
// it to the store. The store is saved to disk before returning.
func (s *Store) Add(start, end, body, source string) (*Comment, error) {
	if err := validateAnchors(start, end, source); err != nil {
		return nil, err
	}

	id, err := s.generateID()
	if err != nil {
		return nil, err
	}

	comment := Comment{
		ID:        id,
		Start:     start,
		End:       end,
		Body:      body,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	s.comments = append(s.comments, comment)

	if err := s.Save(); err != nil {
		// Roll back the in-memory append so the store stays consistent.
		s.comments = s.comments[:len(s.comments)-1]
		return nil, err
	}

	return &comment, nil
}

// Edit updates an existing comment identified by id. Nil pointer arguments
// leave the corresponding field unchanged. Anchor revalidation runs only when
// start or end is non-nil.
func (s *Store) Edit(id string, start, end, body *string, source string) (*Comment, error) {
	index := s.findIndex(id)
	if index == -1 {
		return nil, fmt.Errorf("comment %q not found", id)
	}

	updated := s.comments[index]

	if start != nil {
		updated.Start = *start
	}
	if end != nil {
		updated.End = *end
	}
	if body != nil {
		updated.Body = *body
	}

	// Only revalidate anchors when at least one anchor field changed.
	if start != nil || end != nil {
		if err := validateAnchors(updated.Start, updated.End, source); err != nil {
			return nil, err
		}
	}

	original := s.comments[index]
	s.comments[index] = updated

	if err := s.Save(); err != nil {
		s.comments[index] = original
		return nil, err
	}

	return &updated, nil
}

// Delete removes the comment with the given id from the store and saves.
func (s *Store) Delete(id string) error {
	index := s.findIndex(id)
	if index == -1 {
		return fmt.Errorf("comment %q not found", id)
	}

	original := make([]Comment, len(s.comments))
	copy(original, s.comments)

	s.comments = append(s.comments[:index], s.comments[index+1:]...)

	if err := s.Save(); err != nil {
		s.comments = original
		return err
	}

	return nil
}

// Clear removes all comments by deleting the sidecar file from disk and
// zeroing the in-memory slice. It returns the number of comments that were
// present before clearing. If the sidecar does not exist, it returns 0 with
// no error (idempotent).
//
// The sidecar is deleted rather than overwritten with an empty document
// because the file currently holds nothing but the comments array. If a
// future change adds other top-level fields, this method should be reworked
// to empty the array in place and preserve the rest (see gnosis mkrpzw).
func (s *Store) Clear() (int, error) {
	count := len(s.comments)

	if err := os.Remove(s.sidecarPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("removing sidecar: %w", err)
		}
		// The sidecar was already gone (external delete or race between Load and
		// Clear). The store is logically empty either way, so report 0 rather than
		// the stale in-memory count to avoid misleading output like "cleared 5
		// comments" for what was effectively a no-op on disk.
		s.comments = nil
		return 0, nil
	}

	s.comments = nil
	return count, nil
}

// List returns a copy of all comments in the store.
func (s *Store) List() []Comment {
	result := make([]Comment, len(s.comments))
	copy(result, s.comments)
	return result
}

// validateAnchors checks that start and end each appear exactly once in source
// and that the end anchor does not fall inside the start anchor's span.
func validateAnchors(start, end, source string) error {
	startCount := countOverlapping(source, start)
	switch {
	case startCount == 0:
		return errors.New("start anchor not found in document")
	case startCount > 1:
		return fmt.Errorf("start anchor matches %d places, make it more specific", startCount)
	}

	endCount := countOverlapping(source, end)
	switch {
	case endCount == 0:
		return errors.New("end anchor not found in document")
	case endCount > 1:
		return fmt.Errorf("end anchor matches %d places, make it more specific", endCount)
	}

	startIndex := strings.Index(source, start)
	endIndex := strings.Index(source, end)

	if start == end {
		// Same anchor for both sides: the span is exactly that single match, which
		// is always valid.
		return nil
	}

	// Require the end anchor to begin at or after the end of the start anchor so
	// that the end cannot fall inside the start (e.g. start="The quick brown fox",
	// end="quick" would otherwise pass the old endIndex+len(end) <= startIndex check).
	if endIndex < startIndex+len(start) {
		return errors.New("end anchor appears before or inside start anchor")
	}

	return nil
}

// countOverlapping counts how many times sub appears in s, including overlapping
// occurrences. strings.Count only counts non-overlapping matches, so "aa" in
// "aaa" returns 1 there but 2 here.
func countOverlapping(s, sub string) int {
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

// generateID produces a unique "c-" + 4 lowercase alphanumeric ID, retrying on
// collision with an existing comment in the store.
func (s *Store) generateID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	const length = 4

	for {
		var suffix [length]byte
		for i := range suffix {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", fmt.Errorf("generating random ID: %w", err)
			}
			suffix[i] = alphabet[n.Int64()]
		}

		id := "c-" + string(suffix[:])

		if s.findIndex(id) == -1 {
			return id, nil
		}
	}
}

// findIndex returns the slice index of the comment with the given id, or -1.
func (s *Store) findIndex(id string) int {
	for i, comment := range s.comments {
		if comment.ID == id {
			return i
		}
	}
	return -1
}

// DocID returns the 16-hex document identifier for the given markdown file.
// This is the same hash prefix used to name the sidecar file, exposed here so
// the server can embed it in the page for the client to scope localStorage keys
// per document without a round-trip.
//
// Callers must ensure the markdown file exists on disk before calling DocID,
// because filepath.EvalSymlinks requires the path to be present.
func DocID(mdPath string) (string, error) {
	absPath, err := filepath.Abs(mdPath)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path of %s: %w", mdPath, err)
	}

	// Resolve symlinks so that two paths pointing at the same inode share one
	// document ID. EvalSymlinks requires the file to exist.
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("resolving symlinks for %s: %w", absPath, err)
	}

	sum := sha256.Sum256([]byte(resolvedPath))
	return hex.EncodeToString(sum[:])[:16], nil
}

// SidecarPath returns the path to the sidecar file for the given markdown file.
// The sidecar lives in <UserCacheDir>/graphe/<hash>.graphe, where <hash> is the
// first 16 hex characters of the SHA-256 of the symlink-resolved absolute path
// of the markdown file. This keeps sidecars out of source directories and
// ensures two files with the same basename in different directories get separate
// sidecars.
//
// Callers must ensure the markdown file exists on disk before calling
// SidecarPath, because filepath.EvalSymlinks requires the path to be present.
func SidecarPath(mdPath string) (string, error) {
	docID, err := DocID(mdPath)
	if err != nil {
		return "", err
	}

	cacheBase, err := userCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating user cache directory: %w", err)
	}

	return filepath.Join(cacheBase, "graphe", docID+".graphe"), nil
}

// EnsureCacheDir creates the graphe cache directory if it does not already
// exist. The watcher needs the directory to exist before calling watcher.Add,
// so the server calls this at startup rather than waiting for the first Save.
func EnsureCacheDir() error {
	cacheBase, err := userCacheDir()
	if err != nil {
		return fmt.Errorf("locating user cache directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheBase, "graphe"), 0o700); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}
	return nil
}
