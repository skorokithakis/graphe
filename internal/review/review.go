// SPDX-License-Identifier: AGPL-3.0-or-later

// Package review handles the comment store for a single markdown post.
// Each post's comments are persisted in a sidecar JSON file alongside the
// markdown file (e.g. post.md -> post-review.json).
package review

import (
	"crypto/rand"
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
	// sidecarPath is the path to the JSON file (derived from the .md path).
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
	sidecarPath := sidecarPathFor(mdPath)

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
func (s *Store) Save() error {
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

// List returns a copy of all comments in the store.
func (s *Store) List() []Comment {
	result := make([]Comment, len(s.comments))
	copy(result, s.comments)
	return result
}

// validateAnchors checks that start and end each appear exactly once in source
// and that start does not appear after end.
func validateAnchors(start, end, source string) error {
	startCount := strings.Count(source, start)
	switch {
	case startCount == 0:
		return errors.New("start anchor not found in document")
	case startCount > 1:
		return fmt.Errorf("start anchor matches %d places, make it more specific", startCount)
	}

	endCount := strings.Count(source, end)
	switch {
	case endCount == 0:
		return errors.New("end anchor not found in document")
	case endCount > 1:
		return fmt.Errorf("end anchor matches %d places, make it more specific", endCount)
	}

	startIndex := strings.Index(source, start)
	endIndex := strings.Index(source, end)

	if endIndex+len(end) <= startIndex {
		return errors.New("end anchor appears before start anchor")
	}

	return nil
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

// sidecarPathFor derives the sidecar JSON path from a markdown file path.
// "/path/to/post.md" -> "/path/to/post-review.json".
func sidecarPathFor(mdPath string) string {
	// filepath.Ext only considers dots within the final path element, which
	// avoids incorrectly stripping at a dot in a parent directory name.
	ext := filepath.Ext(mdPath)
	return strings.TrimSuffix(mdPath, ext) + "-review.json"
}
