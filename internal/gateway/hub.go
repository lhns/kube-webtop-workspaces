// SPDX-License-Identifier: Apache-2.0

package gateway

import "sync"

// Hub wakes up SSE streams when the Desktop they watch changes.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan struct{}]struct{}{}} }

// Subscribe returns a channel that receives a (coalesced) signal on every Notify of name.
func (h *Hub) Subscribe(name string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[name] == nil {
		h.subs[name] = map[chan struct{}]struct{}{}
	}
	h.subs[name][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[name], ch)
		if len(h.subs[name]) == 0 {
			delete(h.subs, name)
		}
		h.mu.Unlock()
	}
}

// Notify signals every subscriber of name without blocking.
func (h *Hub) Notify(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[name] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
