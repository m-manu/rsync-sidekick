package lib

import (
	"iter"
	"sync"
)

// SafeMap is a map on which writes are goroutine-safe
type SafeMap[K comparable, V any] struct {
	mx   *sync.Mutex
	data map[K]V
}

// NewSafeMap creates new SafeMap
func NewSafeMap[K comparable, V any]() (m SafeMap[K, V]) {
	return SafeMap[K, V]{
		data: map[K]V{},
		mx:   &sync.Mutex{},
	}
}

// Get gets value for the given key
func (m SafeMap[K, V]) Get(key K) V {
	return m.data[key]
}

// Set sets value for a given key in a goroutine-safe way
func (m SafeMap[K, V]) Set(key K, value V) {
	m.mx.Lock()
	m.data[key] = value
	m.mx.Unlock()
}

// Len returns number of elements in map
func (m SafeMap[K, V]) Len() int {
	return len(m.data)
}

// ForEach iterates over the safe map
func (m SafeMap[K, V]) ForEach() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for k, v := range m.data {
			if !yield(k, v) {
				return
			}
		}
	}
}
