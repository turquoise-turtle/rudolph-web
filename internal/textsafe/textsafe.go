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
	zwnj, zwj                = 0x200c, 0x200d
	// Hearts, keycaps and many other emoji need the emoji presentation selector (VS16) to show as emoji.
	emojiSelector = 0xfe0f
	brailleGap    = 0x2800
)

// String returns s with every character that is a control, or that hides or reorders text, written as a visible
// escape: C0 controls and DEL as \xNN (\n, \r and \t by name), C1 controls, invisible format characters (Unicode Cf),
// line and paragraph separators, every other default-ignorable code point (variation selectors, the combining
// grapheme joiner, Khmer inherent vowels, and reserved code points such as U+2065, U+FFF0-FFF8 and the rest of the
// U+E0000-E0FFF block), and characters that render as blank text without being spaces (Hangul fillers and the blank
// braille pattern) as \uXXXX (\UXXXXXXXX above U+FFFF), and bytes that are not UTF-8 as \xNN.
//
// Zero-width (non-)joiners are kept, as emoji sequences and several scripts need them, and so are the emoji
// presentation selector U+FE0F and the tag characters of a subdivision flag (U+1F3F4, 2 to 7 tag letters a-z or
// digits, then the cancel tag U+E007F); tags anywhere else are escaped. Everything else, backslashes included, is left
// as it is, as Rudolph's CLI does.
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
// DEL or C1), a default-ignorable or invisible format character other than the zero-width (non-)joiner and the emoji
// presentation selector, a line or paragraph separator, or a character that renders as blank text without being a
// space. It matches Rudolph's CLI (internal/cli/termsafe).
func Invisible(r rune) bool {
	if r < utf8.RuneSelf {
		// Most values are ASCII, so they skip the table lookups below.
		return r < 0x20 || r == 0x7f
	}
	switch r {
	case zwnj, zwj, emojiSelector:
		return false
	case 0x115f, 0x1160, 0x3164, 0xffa0, brailleGap:
		// Hangul fillers and the blank braille pattern are letters or symbols that render as nothing, so a value can
		// look shorter or blank while it is not.
		return true
	}
	// Unicode derives Default_Ignorable_Code_Point (DerivedCoreProperties.txt) as Other_Default_Ignorable_Code_Point +
	// Cf + Variation_Selector, minus White_Space, U+FFF9-FFFB, U+13430-1343F and Prepended_Concatenation_Mark. Go has
	// no table for the derived property, so its three sources are checked instead. What Unicode subtracts is all Cf,
	// and stays escaped: those characters still hide or rearrange text in a page that renders no annotations,
	// hieroglyph layout or number signs. No White_Space character is in either other source.
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp,
		unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector)
}

// HasFormat reports whether s contains an invisible format character (Unicode Cf) other than the zero-width
// (non-)joiner, which Persian and several Indic scripts need in ordinary names: what the view refuses in a new group
// name.
func HasFormat(s string) bool { return strings.IndexFunc(s, isFormat) >= 0 }

func isFormat(r rune) bool { return r != zwnj && r != zwj && unicode.Is(unicode.Cf, r) }

// HasHidden reports whether s contains a character that a new group name refuses: one String escapes (Invisible), or
// the emoji presentation selector. Shown text keeps that selector for emoji, but a name gains nothing from it, and two
// names differing only by it would look the same (owner approved, 2026-11-19). The zero-width (non-)joiners are
// allowed, as Persian and several Indic scripts need them in ordinary names.
func HasHidden(s string) bool { return strings.IndexFunc(s, Hidden) >= 0 }

// Hidden reports whether HasHidden refuses r.
func Hidden(r rune) bool { return r == emojiSelector || Invisible(r) }
