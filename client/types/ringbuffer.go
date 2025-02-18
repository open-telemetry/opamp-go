package types

import "sync"

// RingBuffer is a simple goroutine-safe ring buffer.
type RingBuffer[T any] struct {
	buffer   []T
	capacity int
	offset   int
	mu       sync.Mutex
}

// NewRingBuffer creates a new RingBuffer with a set capacity.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	return &RingBuffer[T]{
		capacity: capacity,
	}
}

// Insert puts a new item into the RingBuffer.
func (r *RingBuffer[T]) Insert(item T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buffer) < r.capacity {
		r.buffer = append(r.buffer, item)
	} else {
		r.buffer[r.offset] = item
		r.offset = (r.offset + 1) % r.capacity
	}
}

func (r *RingBuffer[T]) read(into []T) int {
	if len(into) > len(r.buffer) {
		into = into[:len(r.buffer)]
	}
	for i := range into {
		into[i] = r.buffer[(r.offset+i)%len(r.buffer)]
	}
	return len(into)
}

// Read reads the items of the RingBuffer in the order they were inserted in.
func (r *RingBuffer[T]) Read(into []T) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read(into)
}

// Drain reads all of the items in the RingBuffer and resets the RingBuffer's
// internal buffer and offset.
func (r *RingBuffer[T]) Drain() []T {
	result := make([]T, r.capacity)

	r.mu.Lock()
	defer r.mu.Unlock()

	read := r.read(result)
	r.buffer = r.buffer[:0]
	r.offset = 0

	return result[:read]
}
