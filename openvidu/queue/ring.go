// Copyright 2026 OpenVidu
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package queue

import "sync"

// Ring is a bounded FIFO queue safe for concurrent use. Push never blocks: when the ring is full the oldest element
// is evicted, so producers keep their pace and the newest data survives an outage of the consumer.
type Ring[T any] struct {
	mutex sync.Mutex
	items []T
	head  int
	size  int
}

// NewRing returns an empty ring holding at most capacity elements.
func NewRing[T any](capacity int) *Ring[T] {
	if capacity < 1 {
		panic("queue: ring capacity must be positive")
	}
	return &Ring[T]{items: make([]T, capacity)}
}

// Cap returns the maximum number of elements.
func (r *Ring[T]) Cap() int {
	return len(r.items)
}

// Len returns the number of queued elements.
func (r *Ring[T]) Len() int {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.size
}

// Push appends value. When the ring is full the oldest element is evicted and returned with dropped set to true.
func (r *Ring[T]) Push(value T) (evicted T, dropped bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.size == len(r.items) {
		evicted = r.items[r.head]
		r.items[r.head] = value
		r.head = (r.head + 1) % len(r.items)
		return evicted, true
	}
	r.items[(r.head+r.size)%len(r.items)] = value
	r.size++
	return evicted, false
}

// Drain removes and returns up to n elements from the front, oldest first; n <= 0 drains every element.
func (r *Ring[T]) Drain(n int) []T {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if n <= 0 || n > r.size {
		n = r.size
	}
	drained := make([]T, n)
	var zero T
	for i := range drained {
		index := (r.head + i) % len(r.items)
		drained[i] = r.items[index]
		r.items[index] = zero
	}
	r.head = (r.head + n) % len(r.items)
	r.size -= n
	return drained
}

// Requeue puts elements back at the front, in their order, ahead of the elements queued since they were drained.
// When they do not all fit, the oldest of them are dropped; the number dropped is returned.
func (r *Ring[T]) Requeue(front []T) int {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	free := len(r.items) - r.size
	dropped := 0
	if len(front) > free {
		dropped = len(front) - free
		front = front[dropped:]
	}
	for i := len(front) - 1; i >= 0; i-- {
		r.head = (r.head - 1 + len(r.items)) % len(r.items)
		r.items[r.head] = front[i]
		r.size++
	}
	return dropped
}
