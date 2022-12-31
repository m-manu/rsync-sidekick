package lib

// Set is a collection of unique elements
type Set[T comparable] map[T]struct{}

// SetOf creates a Set with given elements
func SetOf[T comparable](s ...T) Set[T] {
	set := make(Set[T], len(s))
	for i := 0; i < len(s); i++ {
		set[s[i]] = struct{}{}
	}
	return set
}

// NewSet creates a Set of given size
func NewSet[T comparable](size int) Set[T] {
	return make(Set[T], size)
}

// Add adds element to Set
func (set Set[T]) Add(e T) {
	set[e] = struct{}{}
}

func (set Set[T]) Exists(e T) bool {
	_, exists := set[e]
	return exists
}
