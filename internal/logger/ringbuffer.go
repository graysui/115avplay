package logger

import (
	"sync"
	"time"
)

// LogEntry represents a single structured log item.
type LogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"`
	Message   string                 `json:"message"`
	Attrs     map[string]interface{} `json:"attrs,omitempty"`
}

// RingBuffer stores a bounded number of LogEntry items with subscriber support.
type RingBuffer struct {
	mu          sync.RWMutex
	capacity    int
	entries     []LogEntry
	start       int
	count       int
	subscribers map[chan LogEntry]struct{}
}

// NewRingBuffer creates a RingBuffer with specified capacity (e.g. 2000).
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 2000
	}
	return &RingBuffer{
		capacity:    capacity,
		entries:     make([]LogEntry, capacity),
		subscribers: make(map[chan LogEntry]struct{}),
	}
}

// Add appends a new entry to the ring buffer and notifies subscribers.
func (r *RingBuffer) Add(entry LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	idx := (r.start + r.count) % r.capacity
	if r.count < r.capacity {
		r.entries[idx] = entry
		r.count++
	} else {
		// Overwrite the oldest entry
		r.entries[r.start] = entry
		r.start = (r.start + 1) % r.capacity
	}

	// Notify active subscribers without blocking
	for ch := range r.subscribers {
		select {
		case ch <- entry:
		default:
			// Non-blocking drop if subscriber is too slow
		}
	}
}

// GetAll returns a slice of all stored entries in chronological order.
func (r *RingBuffer) GetAll() []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]LogEntry, r.count)
	for i := 0; i < r.count; i++ {
		idx := (r.start + i) % r.capacity
		result[i] = r.entries[idx]
	}
	return result
}

// Subscribe returns a channel that receives newly added log entries.
func (r *RingBuffer) Subscribe(bufSize int) chan LogEntry {
	if bufSize <= 0 {
		bufSize = 100
	}
	ch := make(chan LogEntry, bufSize)

	r.mu.Lock()
	r.subscribers[ch] = struct{}{}
	r.mu.Unlock()

	return ch
}

// Unsubscribe removes the subscriber channel.
func (r *RingBuffer) Unsubscribe(ch chan LogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.subscribers, ch)
	close(ch)
}
