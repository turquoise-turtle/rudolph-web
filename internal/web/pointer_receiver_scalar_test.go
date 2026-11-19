package web

import (
	"strings"
	"testing"
)

// ptrStringerNum and ptrErrorNum are numbers whose String or Error method has a pointer receiver. html/template prints
// a pointer to one with that method, but writes the number itself into a script, since it dereferences the pointer
// before looking for a JSON form.
type ptrStringerNum int

func (n *ptrStringerNum) String() string { return "a" + rlo + "b" }

type ptrErrorNum int

func (e *ptrErrorNum) Error() string { return "a" + rlo + "b" }

// A pointer to such a number used to be passed on as it was, because the number it points to has no String or Error
// method, so its text reached the page with the bidi override raw.
func TestShow_PointerReceiverNumberText(t *testing.T) {
	sn, en := ptrStringerNum(1), ptrErrorNum(1)
	shown := "a" + "\\" + "u" + "202eb"
	for name, v := range map[string]any{"String": &sn, "Error": &en} {
		t.Run(name, func(t *testing.T) {
			const text = `<p>{{.}}</p>`
			got, err := renderRewritten(text, v)
			if err != nil {
				t.Fatal(err)
			}
			if hasRawInvisible(got) || strings.Contains(strings.ToLower(got), "&#x202e;") || !strings.Contains(got, shown) {
				t.Errorf("text not shown escaped: %q", got)
			}
			const script = `<script>var x = {{.}};</script>`
			plain := renderPlain(t, script, v)
			got, err = renderRewritten(script, v)
			if err != nil {
				t.Fatal(err)
			}
			if got != plain {
				t.Errorf("script: got %q, html/template writes %q", got, plain)
			}
		})
	}
}
