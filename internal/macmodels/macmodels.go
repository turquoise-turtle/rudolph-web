// Package macmodels turns a Mac's model identifier, such as Mac16,12, into
// Apple's marketing name for it.
//
// The table, models.tsv, is copied by hand from Apple's "Identify your
// MacBook Air model", "… MacBook Pro model", "… MacBook model", "… iMac
// model", "… Mac mini model", "… Mac Studio model" and "… Mac Pro model"
// support pages (support.apple.com/en-us/102869, 108052, 103257, 108054,
// 102852, 102231 and 102887). It is embedded so a page never waits on the
// network, and is refreshed by hand when Apple releases new Macs.
package macmodels

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed models.tsv
var table string

var names = sync.OnceValue(func() map[string]string {
	m := map[string]string{}
	for line := range strings.Lines(table) {
		id, name, ok := strings.Cut(strings.TrimRight(line, "\r\n"), "\t")
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if !ok || id == "" || name == "" {
			continue
		}
		// Apple reuses some identifiers across model years.
		if prev, seen := m[id]; seen {
			name = prev + " or " + name
		}
		m[id] = name
	}
	return m
})

// Name returns Apple's name for a model identifier, or "" for one the table
// does not know.
func Name(identifier string) string {
	return names()[strings.TrimSpace(identifier)]
}
