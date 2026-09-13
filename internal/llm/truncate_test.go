// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the char-cap article truncation utility.
//
// Truncation is the single biggest "make-or-break" cost control on
// the LLM side: an unbounded article body can blow the context
// window, hit per-token billing, and silently truncate server-side
// anyway. We want to cut deterministically, at a word boundary,
// and without breaking on multibyte UTF-8.

package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncate_UnderLimitReturnsAsIs(t *testing.T) {
	in := "short text"
	got := Truncate(in, 100)
	if got != in {
		t.Errorf("Truncate: got %q, want %q", got, in)
	}
}

func TestTruncate_ExactlyAtLimitReturnsAsIs(t *testing.T) {
	in := "exactly forty-four characters long this is twelve"
	got := Truncate(in, utf8.RuneCountInString(in))
	if got != in {
		t.Errorf("Truncate: got %q, want %q", got, in)
	}
}

func TestTruncate_OverLimitCutsAtWordBoundary(t *testing.T) {
	in := "the quick brown fox jumps over the lazy dog"
	// cap at 20 chars: "the quick brown fox " = 20 chars (no, 20 = "the quick brown fox")
	got := Truncate(in, 20)
	if got != "the quick brown fox" {
		t.Errorf("Truncate: got %q, want %q", got, "the quick brown fox")
	}
}

func TestTruncate_OverLimitWithLongWordFallsBackToHardCut(t *testing.T) {
	// When the first "word" alone exceeds the cap, we have to
	// hard-cut. The cut must still respect UTF-8 boundaries.
	in := "supercalifragilisticexpialidocious is long"
	got := Truncate(in, 5)
	// We expect either the first 5 runes ("super") or, if our
	// word-boundary logic returns "" (no space before cap), we
	// fall back to the prefix.
	if got != "super" {
		t.Errorf("Truncate: got %q, want %q", got, "super")
	}
}

func TestTruncate_EmptyString(t *testing.T) {
	got := Truncate("", 100)
	if got != "" {
		t.Errorf("Truncate: got %q, want \"\"", got)
	}
}

func TestTruncate_ZeroCapReturnsEmpty(t *testing.T) {
	// Zero cap means "truncate to nothing" — the caller can detect
	// this and skip the call entirely. Documenting the behaviour.
	got := Truncate("hello world", 0)
	if got != "" {
		t.Errorf("Truncate: got %q, want \"\"", got)
	}
}

func TestTruncate_NegativeCapReturnsEmpty(t *testing.T) {
	got := Truncate("hello world", -5)
	if got != "" {
		t.Errorf("Truncate: got %q, want \"\"", got)
	}
}

func TestTruncate_UnicodeSafe(t *testing.T) {
	// 4-byte emoji + 1 byte (ASCII space). Rune count is what matters.
	emoji := "\U0001F600" // 😀
	in := emoji + " " + emoji + " " + emoji
	// 3 emojis + 2 spaces = 5 runes
	if utf8.RuneCountInString(in) != 5 {
		t.Fatalf("test setup wrong: rune count = %d", utf8.RuneCountInString(in))
	}
	got := Truncate(in, 3) // cuts at the first space, keeping "😀"
	if got != emoji {
		t.Errorf("Truncate: got %q (len %d), want %q", got, len(got), emoji)
	}
	// Critical: the result must be valid UTF-8.
	if !utf8.ValidString(got) {
		t.Errorf("Truncate: result is not valid UTF-8: % x", []byte(got))
	}
}

func TestTruncate_PrefersLastSpaceBeforeCap(t *testing.T) {
	// Cap falls in the middle of "gamma". We should walk back to
	// the space at index 10 (after "beta") rather than mid-word.
	in := "alpha beta gamma delta epsilon"
	got := Truncate(in, 14) // "alpha beta gam" — last full word boundary at 10
	want := "alpha beta"
	if got != want {
		t.Errorf("Truncate: got %q, want %q", got, want)
	}
}

func TestTruncate_AddsEllipsisMarker(t *testing.T) {
	// Callers may want to know that truncation happened. The marker
	// is configurable via a separate option; default behaviour is
	// to NOT add it (callers can call WasTruncated separately).
	in := "this is a moderately long sentence that we will truncate"
	got := Truncate(in, 10)
	if strings.Contains(got, "…") {
		t.Errorf("Truncate: default should not add ellipsis, got %q", got)
	}
}

func TestTruncate_HandlesMultipleParagraphs(t *testing.T) {
	in := "first paragraph text here\n\nsecond paragraph follows"
	got := Truncate(in, 18) // "first paragraph " — first space before cap
	if !strings.HasPrefix(got, "first paragraph") {
		t.Errorf("Truncate: got %q", got)
	}
}

func TestTruncate_NoTrailingSpace(t *testing.T) {
	in := "hello world this is a test"
	got := Truncate(in, 11) // last space at 11
	if strings.HasSuffix(got, " ") {
		t.Errorf("Truncate: result should not end in space, got %q", got)
	}
}

func TestWasTruncated_ReportsIfCut(t *testing.T) {
	cases := []struct {
		in     string
		cap    int
		expect bool
	}{
		{"short", 100, false},
		{"exact", 5, false},
		{"this is longer", 4, true},
		{"", 10, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := WasTruncated(tc.in, tc.cap)
			if got != tc.expect {
				t.Errorf("WasTruncated(%q, %d): got %v, want %v",
					tc.in, tc.cap, got, tc.expect)
			}
		})
	}
}
