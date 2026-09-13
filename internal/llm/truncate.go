// SPDX-License-Identifier: GPL-3.0-or-later

package llm

import "unicode/utf8"

// Truncate returns the longest prefix of s that contains at most max
// runes AND ends on a word boundary (a space). If no space exists
// before the cap, falls back to a hard cut at exactly max runes.
//
// Both the cap and the returned length are measured in runes, not
// bytes, so multibyte UTF-8 (CJK, emoji, accented characters) is
// counted correctly. The result is always valid UTF-8.
//
// max <= 0 returns an empty string.
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}

	// Walk the prefix to max runes, then search backwards for the
	// last space rune. utf8.DecodeRuneInString is allocation-free
	// and tells us the byte width of each rune.
	count := 0
	byteIdx := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		count++
		i += size
		if count == max {
			byteIdx = i
			break
		}
	}

	// byteIdx is now the byte offset of the rune at index max.
	prefix := s[:byteIdx]
	if i := lastSpace(prefix); i >= 0 {
		return prefix[:i]
	}
	return prefix
}

// lastSpace returns the byte index of the last space rune in s, or
// -1 if no space rune exists. Operates on bytes since the space
// rune is single-byte in UTF-8.
func lastSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return -1
}

// WasTruncated reports whether Truncate(s, max) would change s.
// Useful for callers that want to log "we cut X chars" without
// running the full cut twice.
func WasTruncated(s string, max int) bool {
	if max <= 0 {
		return s != ""
	}
	return utf8.RuneCountInString(s) > max
}
