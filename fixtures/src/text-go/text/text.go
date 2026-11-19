// Package text provides small, dependency-free string helpers.
package text

import "strings"

// Reverse returns s with its characters in reverse order.
func Reverse(s string) string {
	out := make([]byte, 0, len(s))
	for i := len(s) - 1; i >= 0; i-- {
		out = append(out, s[i])
	}
	return string(out)
}

// WordCount returns how many times each word appears in s.
//
// A word is a run of letters or digits. Words are compared case-insensitively.
func WordCount(s string) map[string]int {
	counts := map[string]int{}
	for _, word := range strings.Split(s, " ") {
		if word == "" {
			continue
		}
		counts[strings.ToLower(word)]++
	}
	return counts
}

// Truncate shortens s to at most n characters.
//
// TODO: not implemented yet; the current version returns s unchanged.
func Truncate(s string, n int) string {
	return s
}

// Dedupe returns the unique values in values, preserving first occurrence.
//
// TODO: not implemented yet; the current version returns values unchanged.
func Dedupe(values []string) []string {
	return values
}

// NormalizeSpace collapses runs of whitespace into single spaces and trims
// leading and trailing whitespace.
//
// TODO: not implemented yet; the current version returns s unchanged.
func NormalizeSpace(s string) string {
	return s
}

// SnakeCase converts a title-like string to snake_case.
//
// TODO: not implemented yet; the current version returns s unchanged.
func SnakeCase(s string) string {
	return s
}
