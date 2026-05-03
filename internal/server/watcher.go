// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceDelay is how long to wait after the last file event before reloading.
// Editors often emit multiple events per save (e.g., truncate then write), so
// we coalesce them into a single reload rather than hammering the disk.
const debounceDelay = 150 * time.Millisecond

// Watch starts a goroutine that watches the markdown file and its .graphe
// sidecar for changes. When a relevant file changes, it debounces the events,
// calls Reload, and broadcasts a "reload" SSE event to all connected clients.
//
// Watch returns when ctx is cancelled. The caller is responsible for providing
// a context that is cancelled when the server should stop (e.g., on SIGINT).
func (s *Server) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Watch the parent directory rather than the files directly. Editors that
	// use atomic saves (write to a temp file then rename) would cause a
	// directly-watched file to lose its watch after the rename. Watching the
	// directory catches Create/Rename events for both the .md and the
	// .graphe sidecar regardless of whether the sidecar exists at startup.
	directory := filepath.Dir(s.mdPath)
	if err := watcher.Add(directory); err != nil {
		return err
	}

	// The two filenames we care about: the markdown source and its sidecar.
	mdBase := filepath.Base(s.mdPath)
	sidecarBase := sidecarBasename(s.mdPath)

	var debounceTimer *time.Timer

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return nil

		case event, open := <-watcher.Events:
			if !open {
				return nil
			}

			base := filepath.Base(event.Name)
			if base != mdBase && base != sidecarBase {
				// Unrelated file in the same directory; ignore.
				continue
			}

			// Chmod-only events carry no content change; skip them.
			if event.Op == fsnotify.Chmod {
				continue
			}

			// (Re)start the debounce timer. If another event arrives before it
			// fires, we reset it so only the final event in a burst triggers a
			// reload.
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(debounceDelay, func() {
				if err := s.Reload(); err != nil {
					// Keep the previously-rendered state on transient errors
					// (e.g., file mid-write). The next event will retry.
					log.Printf("watcher: reload failed: %v", err)
					return
				}
				s.broadcaster.Broadcast("reload")
			})

		case err, open := <-watcher.Errors:
			if !open {
				return nil
			}
			log.Printf("watcher: fsnotify error: %v", err)
		}
	}
}

// sidecarBasename returns the basename of the .graphe sidecar for the
// given markdown path. For example, "post.md" → "post.graphe".
func sidecarBasename(mdPath string) string {
	base := filepath.Base(mdPath)
	ext := filepath.Ext(base)
	stem := base[:len(base)-len(ext)]
	return stem + ".graphe"
}
