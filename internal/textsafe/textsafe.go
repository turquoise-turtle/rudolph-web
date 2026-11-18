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

// String returns s with every character that is a control, or that hides or reorders text, written as a visible
// escape: C0 controls and DEL as \xNN (\n, \r and \t by name), C1 controls, invisible format characters (Unicode Cf)
// and line and paragraph separators as \uXXXX (\UXXXXXXXX above U+FFFF), and bytes that are not UTF-8 as \xNN.
// Zero-width (non-)joiners are kept, as emoji sequences and several scripts need them; everything else, backslashes
// included, is left as it is, as Rudolph's CLI does.
func String(s string) string { return escape(s, false) }

// Lines is String, except that line feeds are kept, for text shown with its line breaks (such as a compiler's error
// message pointing at a column).
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

// Invisible reports whether String escapes r: a control character (C0, DEL or C1), an invisible format character
// other than the zero-width (non-)joiner, or a line or paragraph separator.
func Invisible(r rune) bool {
	switch {
	case r == 0x200c || r == 0x200d:
		return false
	case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
		return true
	}
	return false
}

// HasFormat reports whether s contains an invisible format character (Unicode Cf) other than the zero-width
// (non-)joiner, which Persian and several Indic scripts need in ordinary names: what the view refuses in a new group
// name.
func HasFormat(s string) bool { return strings.IndexFunc(s, isFormat) >= 0 }

func isFormat(r rune) bool { return r != 0x200c && r != 0x200d && unicode.Is(unicode.Cf, r) }
