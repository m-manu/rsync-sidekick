package entity

// StringSet is a set of string elements
type StringSet map[string]struct{}

// StringSetOf creates a set with given elements
func StringSetOf(s ...string) StringSet {
	set := make(StringSet, len(s))
	for i := 0; i < len(s); i++ {
		set[s[i]] = struct{}{}
	}
	return set
}

// NewStringSet creates a set of given size
func NewStringSet(size int) StringSet {
	return make(StringSet, size)
}

// Add adds element to set
func (set StringSet) Add(e string) {
	set[e] = struct{}{}
}
