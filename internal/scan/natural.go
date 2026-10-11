package scan

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// NaturalLess reports whether a sorts before b in case-insensitive natural
// order: runs of ASCII digits compare by numeric value ("img2" < "img10").
// Names that are equal under that rule are ordered bytewise so the result is
// a strict total order.
func NaturalLess(a, b string) bool {
	if c := naturalCompare(a, b); c != 0 {
		return c < 0
	}
	return a < b
}

func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			na, ra := digitRun(a)
			nb, rb := digitRun(b)
			if c := compareNumbers(na, nb); c != 0 {
				return c
			}
			a, b = ra, rb
			continue
		}
		ca, sa := utf8.DecodeRuneInString(a)
		cb, sb := utf8.DecodeRuneInString(b)
		if ca, cb = unicode.ToLower(ca), unicode.ToLower(cb); ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		a, b = a[sa:], b[sb:]
	}
	return len(a) - len(b) // the shorter (a prefix of the other) comes first
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func digitRun(s string) (run, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}

// compareNumbers compares two digit strings of any length by value.
func compareNumbers(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}
