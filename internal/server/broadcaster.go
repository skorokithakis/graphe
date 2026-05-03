// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import "sync"

// broadcaster distributes string event names to all currently subscribed SSE
// clients. The live-reload ticket (gra-dmtfx) will call Broadcast("reload")
// when fsnotify detects a file change; for now no events are sent and the SSE
// handler simply waits on the channel until the client disconnects.
type broadcaster struct {
	mu          sync.Mutex
	subscribers map[chan string]struct{}
	// closed flips to true once closeAll has run. Subsequent subscribe calls
	// return an already-closed channel so any handler started during shutdown
	// exits its loop immediately rather than blocking forever.
	closed bool
}

func newBroadcaster() *broadcaster {
	return &broadcaster{
		subscribers: make(map[chan string]struct{}),
	}
}

// subscribe returns a channel that will receive event names sent via Broadcast.
// The caller must call unsubscribe when done.
func (b *broadcaster) subscribe() chan string {
	// Buffer of 8 so a slow client does not block Broadcast.
	channel := make(chan string, 8)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		// Race with shutdown: hand back a closed channel so the caller's
		// for-select sees `open == false` on the first receive and returns.
		close(channel)
		return channel
	}
	b.subscribers[channel] = struct{}{}
	return channel
}

// unsubscribe removes the channel from the subscriber set and closes it. It is
// a no-op if the channel was already removed (e.g. by closeAll during
// shutdown), so handlers can defer unsubscribe unconditionally.
func (b *broadcaster) unsubscribe(channel chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subscribers[channel]; !ok {
		return
	}
	delete(b.subscribers, channel)
	close(channel)
}

// Broadcast sends event to all current subscribers. Subscribers whose buffers
// are full are skipped to avoid blocking the caller.
func (b *broadcaster) Broadcast(event string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for channel := range b.subscribers {
		select {
		case channel <- event:
		default:
			// Subscriber is not keeping up; skip rather than block.
		}
	}
}

// closeAll closes every subscriber channel and marks the broadcaster as closed
// so future subscribers also receive a closed channel. This is the mechanism
// by which graceful shutdown unblocks the SSE handlers: http.Server.Shutdown
// does not cancel in-flight request contexts, so the long-lived /events
// handler would otherwise sit on the receive until the shutdown timeout fires.
func (b *broadcaster) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for channel := range b.subscribers {
		close(channel)
	}
	b.subscribers = nil
}
