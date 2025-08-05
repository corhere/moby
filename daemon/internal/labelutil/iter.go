package labelutil

import (
	"iter"
	"strings"
)

// All converts "key=value" to a sequence of key-value pairs.
func All(seq iter.Seq[string]) iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		for label := range seq {
			k, v, _ := strings.Cut(label, "=")
			if !yield(k, v) {
				return
			}
		}
	}
}
