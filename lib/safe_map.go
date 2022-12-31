package lib

import "sync"

// SafeMap is a map on which writes are goroutine-safe
type SafeMap[K comparable, V any] struct {
	mx   *sync.Mutex
	Data map[K]V
}

// NewSafeMap creates new SafeMap
func NewSafeMap[K comparable, V any]() (m SafeMap[K, V]) {
	return SafeMap[K, V]{
		Data: map[K]V{},
		mx:   &sync.Mutex{},
	}
}

// Get gets value for given key
func (m SafeMap[K, V]) Get(key K) V {
	return m.Data[key]
}

// Set sets value for a given key in a goroutine-safe way
func (m SafeMap[K, V]) Set(key K, value V) {
	m.mx.Lock()
	m.Data[key] = value
	m.mx.Unlock()
}

// Len returns number of elements in map
func (m SafeMap[K, V]) Len() int {
	return len(m.Data)
}
