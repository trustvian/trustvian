package main

// Making an artifact-supplied string inert in GitHub-flavored Markdown.
//
// Every such value is rendered as one code span on one line. Inside a code
// span GitHub renders no link, image, HTML, emphasis, @mention or #issue
// reference, so the value's own Markdown has no effect. What remains is to
// keep the value from leaving its span or its line:
//
//   - every control, format and line-separator character is replaced by
//     U+FFFD, so no newline can start a forged row or section, and no
//     bidirectional override can reorder what a reader sees — and so is every
//     letter that renders as nothing (the Hangul fillers, the blank braille
//     pattern), so a value cannot pass for an empty one;
//   - a value that is only whitespace is shown as *(blank)*, not as an
//     invisible span;
//   - the span's fence is one backtick longer than the longest run of
//     backticks in the value, so the value cannot close it early;
//   - every | is shown as ｜ (U+FF5C). GitHub splits a table row on a pipe
//     even inside a code span, and whether its \| escape survives a
//     preceding backslash is the table scanner's business; with no ASCII
//     pipe in any value, no value can add a cell, whatever precedes it;
//   - the value is cut to a byte limit at a UTF-8 boundary, and a cut value
//     is marked, outside the span, as truncated.

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const truncatedMarker = " *(truncated)*"

// invisibleLetter holds the characters Unicode classes as letters or symbols
// that render as blank space.
var invisibleLetter = map[rune]bool{
	'\u115F': true, '\u1160': true, '\u3164': true, '\uFFA0': true, // Hangul fillers
	'\u2800': true, // braille pattern blank
}

// inert returns s as one inert, single-line code span of at most limit bytes
// of content, the truncation ellipsis included.
func inert(s string, limit int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || invisibleLetter[r] {
			return utf8.RuneError
		}
		return r
	}, s)

	truncated := false
	if len(s) > limit {
		// The ellipsis counts against the limit.
		cut := max(limit-len("…"), 0)
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
		truncated = true
	}
	if s == "" {
		return "*(empty)*"
	}
	if strings.TrimSpace(s) == "" {
		return "*(blank)*"
	}

	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	body := strings.ReplaceAll(s, "|", "\uFF5C")
	// A value that begins or ends with a backtick, or is padded with spaces,
	// needs a space between it and the fence; GitHub strips exactly one from
	// each side.
	if strings.HasPrefix(body, "`") || strings.HasSuffix(body, "`") ||
		strings.HasPrefix(body, " ") || strings.HasSuffix(body, " ") {
		body = " " + body + " "
	}
	out := fence + body + fence
	if truncated {
		out += truncatedMarker
	}
	return out
}
