// Copyright 2024 OpenVidu
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

import "errors"

type Queue[T any] interface {
	Enqueue(T) error
	Dequeue() (T, error)
	Len() int
}

var (
	ErrQueueEmpty = errors.New("queue empty")
	ErrQueueFull  = errors.New("queue full")
)

type SliceQueue[T any] []T

func NewSliceQueue[T any]() Queue[T] {
	return &SliceQueue[T]{}
}

// Len returns the number of elements in the queue.
func (q *SliceQueue[T]) Len() int {
	return len(*q)
}

// Enqueue adds an element to the end of the queue.
func (q *SliceQueue[T]) Enqueue(value T) error {
	*q = append(*q, value)
	return nil
}

// Dequeue removes and returns the first element from the queue.
func (q *SliceQueue[T]) Dequeue() (T, error) {
	queue := *q
	if len(*q) > 0 {
		element := queue[0]
		*q = queue[1:]
		return element, nil
	}

	var empty T
	return empty, ErrQueueEmpty
}
