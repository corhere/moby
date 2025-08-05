package filter

import (
	"encoding/json"
	"iter"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/filters"
)

// Predicate stores a mapping of keys to a set of multiple values.
type Predicate map[string]map[string]bool

// FromJSON decodes a JSON encoded string into Predicate
func PredicateFromJSON(p string) (Predicate, error) {
	args := Predicate{}

	if p == "" {
		return args, nil
	}

	raw := []byte(p)
	err := json.Unmarshal(raw, &args)
	if err == nil {
		return args, nil
	}

	// Fallback to parsing arguments in the legacy slice format
	deprecated := map[string][]string{}
	if legacyErr := json.Unmarshal(raw, &deprecated); legacyErr != nil {
		return args, &invalidFilter{}
	}

	for k, v := range deprecated {
		args.Add(k, v...)
	}
	return args, nil
}

// Deprecated: use maps.Keys(a[key]) or range over a[key]
func (a Predicate) Values(key string) iter.Seq[string] {
	return maps.Keys(a[key])
}

// FIXME: remove
//
// deprecated: don't merge
func (a Predicate) Has(key string) bool {
	return a.Contains(key)
}

// Contains returns true if the key exists in a.
func (a Predicate) Contains(key string) bool {
	_, ok := a[key]
	return ok
}

// Add inserts values into a for key and returns a for chaining.
func (a Predicate) Add(key string, values ...string) Predicate {
	if _, ok := a[key]; !ok {
		a[key] = map[string]bool{}
	}
	for _, v := range values {
		a[key][v] = true
	}
	return a
}

// Del removes values from a for key and returns a for chaining.
func (a Predicate) Del(key string, values ...string) Predicate {
	if _, ok := a[key]; ok {
		for _, v := range values {
			delete(a[key], v)
		}
		if len(a[key]) == 0 {
			delete(a, key)
		}
	}
	return a
}

// Bool returns a boolean value of the key if the key is present and is
// interpretable as a boolean value. Otherwise the default value is returned.
// An error is returned if the filter values are not valid boolean or are
// conflicting.
func (a Predicate) Bool(key string, defaultValue bool) (bool, error) {
	fieldValues, ok := a[key]
	if !ok {
		return defaultValue, nil
	}

	if len(fieldValues) == 0 {
		return defaultValue, &invalidFilter{key, nil}
	}

	isFalse := fieldValues["0"] || fieldValues["false"]
	isTrue := fieldValues["1"] || fieldValues["true"]
	if isFalse == isTrue {
		// Either no or conflicting truthy/falsy value were provided
		return defaultValue, &invalidFilter{key, slices.Collect(a.Values(key))}
	}
	return isTrue, nil
}

// Unique returns an arbitrary value for key and the number of values for key.
func (a Predicate) Unique(key string) (string, int) {
	v := a[key]
	for vv := range v {
		return vv, len(v)
	}
	return "", 0
}

// FilterEqual returns true if any of the values for key are equal to value,
// or if the key does not exist in a.
func (a Predicate) FilterEqual(key, value string) bool {
	fieldValues := a[key]
	if len(fieldValues) == 0 {
		return true
	}

	return fieldValues[value]
}

// Deprecated: use [Predicate.FilterEqual]
func (a Predicate) ExactMatch(key, value string) bool {
	return a.FilterEqual(key, value)
}

// FilterUnique returns true if there is exactly one value for key and it is equal
// to value, or if the key does not exist in a.
func (a Predicate) FilterUnique(key, value string) bool {
	fieldValues := a[key]
	if len(fieldValues) == 0 {
		return true
	}
	if len(fieldValues) != 1 {
		return false
	}

	return fieldValues[value]
}

// FilterPrefix returns true if any of the values for key is a prefix of value,
// or if the key does not exist in a.
func (a Predicate) FilterPrefix(key, value string) bool {
	if a.FilterEqual(key, value) {
		return true
	}

	for prefix := range a[key] {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// FilterRegexpMatch returns true if any of the values for key is a regular
// expression that matches value, or if the key does not exist in a.
func (a Predicate) FilterRegexpMatch(key, value string) bool {
	if a.FilterEqual(key, value) {
		return true
	}

	for re := range a[key] {
		matched, err := regexp.MatchString(re, value)
		if err != nil {
			continue
		}
		if matched {
			return true
		}
	}
	return false
}

// Validate compared the set of accepted keys against the keys in the mapping.
// An error is returned if any mapping keys are not in the accepted set.
func (a Predicate) Validate(accepted map[string]bool) error {
	for name := range a {
		if !accepted[name] {
			return &invalidFilter{name, nil}
		}
	}
	return nil
}

// Clone returns a deep copy of a.
func (a Predicate) Clone() Predicate {
	if a == nil {
		return nil
	}
	clone := make(Predicate, len(a))
	for k, v := range a {
		clone[k] = maps.Clone(v)
	}
	return clone
}

func (a Predicate) APIFilters() filters.Args {
	filtersArgs := filters.NewArgs()
	for k, v := range a {
		for val := range v {
			filtersArgs.Add(k, val)
		}
	}
	return filtersArgs
}

func FromAPIFilters(a filters.Args) Predicate {
	args := make(Predicate)
	for _, k := range a.Keys() {
		args.Add(k, a.Get(k)...)
	}
	return args
}
