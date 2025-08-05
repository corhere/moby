package iterutil

import (
	"iter"
	"maps"
)

// SameValues checks if a and b yield the same values, independent of order.
func SameValues[T comparable](a, b iter.Seq[T]) bool {
	m, n := make(map[T]int), make(map[T]int)
	for v := range a {
		m[v]++
	}
	for v := range b {
		n[v]++
	}
	return maps.Equal(m, n)
}

// Deref adapts an iterator of pointers to an iterator of values.
func Deref[T any, P *T](s iter.Seq[P]) iter.Seq[T] {
	return func(yield func(T) bool) {
		for p := range s {
			if !yield(*p) {
				return
			}
		}
	}
}

// ForEachErr iterates over a sequence and applies a function to each element.
// If the function returns an error, the iteration stops and the error is returned.
func ForEachErr[T any](s iter.Seq[T], f func(T) error) error {
	for v := range s {
		if err := f(v); err != nil {
			return err
		}
	}
	return nil
}
