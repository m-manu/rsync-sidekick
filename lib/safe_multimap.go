package lib

import "sync"

// MultiMap is a multi-map to which writes are go-routine safe
type MultiMap[K comparable, V any] struct {
	mx   *sync.Mutex
	data map[K][]V
}

// NewMultiMap creates new MultiMap
func NewMultiMap[K comparable, V any]() (m MultiMap[K, V]) {
	return MultiMap[K, V]{
		data: map[K][]V{},
		mx:   &sync.Mutex{},
	}
}

// Get gets all values matching the key
func (m MultiMap[K, V]) Get(key K) []V {
	return m.data[key]
}

// Exists checks whether key exists in map
func (m MultiMap[K, V]) Exists(key K) bool {
	_, exists := m.data[key]
	return exists
}

// Set sets a value for the key
func (m MultiMap[K, V]) Set(key K, value V) {
	m.mx.Lock()
	m.data[key] = append(m.data[key], value)
	m.mx.Unlock()
}
