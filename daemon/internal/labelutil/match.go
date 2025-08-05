package labelutil

import (
	"iter"
	"slices"
	"strings"
)

// MatchKVList returns true if all the entries in sources exist as key=value
// pairs in the labels, or if the list of labels is empty.
func MatchKVList(labels []string, sources map[string]string) bool {
	return MatchKVSeq(slices.Values(labels), sources)
}

// MatchKVSeq returns true if all the entries in sources exist as key=value
// pairs in the labels, or if the list of labels is empty.
func MatchKVSeq(labels iter.Seq[string], sources map[string]string) bool {
	if len(sources) == 0 {
		return false
	}

	for label := range labels {
		testK, testV, hasValue := strings.Cut(label, "=")

		v, ok := sources[testK]
		if !ok {
			return false
		}
		if hasValue && testV != v {
			return false
		}
	}

	return true
}
