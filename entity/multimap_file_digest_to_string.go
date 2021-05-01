package entity

import "sync"

// FileDigestStringMultiMap is a multi-map with FileDigest keys and string values.
// Writes to this is goroutine-safe.
type FileDigestStringMultiMap struct {
	mx   *sync.Mutex
	data map[FileDigest][]string
}

// NewFileDigestStringMultiMap creates new FileDigestStringMultiMap
func NewFileDigestStringMultiMap() (m *FileDigestStringMultiMap) {
	return &FileDigestStringMultiMap{
		data: map[FileDigest][]string{},
		mx:   &sync.Mutex{},
	}
}

// Get gets all values matching the key
func (m *FileDigestStringMultiMap) Get(key FileDigest) []string {
	return m.data[key]
}

// Exists checks whether key exists in map
func (m *FileDigestStringMultiMap) Exists(key FileDigest) bool {
	_, exists := m.data[key]
	return exists
}

// Set sets a value for the key
func (m *FileDigestStringMultiMap) Set(key FileDigest, value string) {
	m.mx.Lock()
	m.data[key] = append(m.data[key], value)
	m.mx.Unlock()
}
