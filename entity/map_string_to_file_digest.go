package entity

import "sync"

// StringFileDigestMap is a map with string keys and FileDigest values.
// Writes to this is goroutine-safe.
type StringFileDigestMap struct {
	mx   *sync.Mutex
	data map[string]FileDigest
}

// NewStringFileDigestMap creates new StringFileDigestMap
func NewStringFileDigestMap() (m *StringFileDigestMap) {
	return &StringFileDigestMap{
		data: map[string]FileDigest{},
		mx:   &sync.Mutex{},
	}
}

// Get gets value for given key
func (m *StringFileDigestMap) Get(key string) FileDigest {
	return m.data[key]
}

// Set sets value for a given key in a goroutine-safe way
func (m *StringFileDigestMap) Set(key string, value FileDigest) {
	m.mx.Lock()
	m.data[key] = value
	m.mx.Unlock()
}

// Len returns number of elements in map
func (m *StringFileDigestMap) Len() int {
	return len(m.data)
}

// Map gets underlying map
func (m *StringFileDigestMap) Map() map[string]FileDigest {
	return m.data
}
