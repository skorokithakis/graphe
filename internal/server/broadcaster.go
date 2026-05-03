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
	b.subscribers[channel] = struct{}{}
	b.mu.Unlock()
	return channel
}

// unsubscribe removes the channel from the subscriber set and closes it.
func (b *broadcaster) unsubscribe(channel chan string) {
	b.mu.Lock()
	delete(b.subscribers, channel)
	b.mu.Unlock()
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
