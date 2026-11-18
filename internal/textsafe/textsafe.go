// Package textsafe makes text that Macs reported, or that other tools stored, safe to show to a person.
//
// A host name, file name, path or audit summary can carry control characters and bidi or zero-width characters that
// hide text or make it read as something it is not: "invoice\u202efdp.exe" displays as "invoiceexe.pdf". Such
// characters are shown as visible Go-style escapes (\x1b, \n, \u202e), as Rudolph's CLI shows them (its
// internal/cli/termsafe), so the web view and the CLI agree on what a value looks like.
package textsafe

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	blackFlag = 0x1f3f4
	tagLowerA = 0xe0061
	tagLowerZ = 0xe007a
	tagDigit0 = 0xe0030
	tagDigit9 = 0xe0039
	cancelTag = 0xe007f

	// A subdivision flag's tags spell a region code (Unicode CLDR): 2 to 7 lower-case letters and digits.
	minFlagTags, maxFlagTags = 2, 7
)

// String returns s with every character that is a control, or that hides or reorders text, written as a visible
// escape: C0 controls and DEL as \xNN (\n, \r and \t by name), C1 controls, invisible format characters (Unicode Cf),
// line and paragraph separators, and characters that render as blank text without being spaces (Hangul fillers and
// the blank braille pattern) as \uXXXX (\UXXXXXXXX above U+FFFF), and bytes that are not UTF-8 as \xNN.
//
// Zero-width (non-)joiners are kept, as emoji sequences and several scripts need them, and so are the tag characters
// of a subdivision flag (U+1F3F4, 2 to 7 tag letters a-z or digits, then the cancel tag U+E007F); tags anywhere else
// are escaped. Everything else, backslashes included, is left as it is, as Rudolph's CLI does.
func String(s string) string { return escape(s, false) }

// Lines is String, except that line feeds are kept, for text shown with its line breaks (such as a multi-line CEL
// expression, or a compiler's error message pointing at a column).
func Lines(s string) string { return escape(s, true) }

func escape(s string, keepNewlines bool) string {
	if utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return Invisible(r) && !(keepNewlines && r == '\n') }) < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == blackFlag:
			flag := size + flagTags(s[i+size:])
			b.WriteString(s[i : i+flag])
			size = flag
		case r == '\n' && keepNewlines:
			b.WriteByte('\n')
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case Invisible(r) && r > 0xffff:
			fmt.Fprintf(&b, `\U%08x`, r)
		case Invisible(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// flagTags returns the length in bytes of the tag characters at the start of s that complete a subdivision flag: the
// 2 to 7 tag letters (a-z) and digits of a region code such as gbwls, then the cancel tag. It returns 0 for any other
// tag text, which would show as a plain flag while hiding what it spells, so its tags are escaped.
func flagTags(s string) int {
	n, count := 0, 0
	for n < len(s) {
		r, size := utf8.DecodeRuneInString(s[n:])
		switch {
		case r == cancelTag && count >= minFlagTags:
			return n + size
		case count < maxFlagTags && ((r >= tagLowerA && r <= tagLowerZ) || (r >= tagDigit0 && r <= tagDigit9)):
			n += size
			count++
		default:
			return 0
		}
	}
	return 0
}

// Invisible reports whether String escapes r wherever it appears outside a subdivision flag: a control character (C0,
// DEL or C1), an invisible format character other than the zero-width (non-)joiner, a line or paragraph separator, or
// a character that renders as blank text without being a space.
func Invisible(r rune) bool {
	switch r {
	case 0x200c, 0x200d:
		return false
	case 0x115f, 0x1160, 0x3164, 0xffa0, 0x2800:
		// Hangul fillers and the blank braille pattern are letters or symbols that render as nothing, so a value can
		// look shorter or blank while it is not.
		return true
	}
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// HasFormat reports whether s contains an invisible format character (Unicode Cf) other than the zero-width
// (non-)joiner, which Persian and several Indic scripts need in ordinary names: what the view refuses in a new group
// name.
func HasFormat(s string) bool { return strings.IndexFunc(s, isFormat) >= 0 }

func isFormat(r rune) bool { return r != 0x200c && r != 0x200d && unicode.Is(unicode.Cf, r) }
