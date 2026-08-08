package domain

import (
	"maps"
	"slices"
)

func SortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
