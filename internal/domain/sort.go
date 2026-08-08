package domain

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

func SortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// JoinPairs выводит map как "ключ=значение ..." в порядке ключей.
func JoinPairs[V any](m map[string]V) string {
	parts := make([]string, 0, len(m))
	for _, k := range SortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, " ")
}
