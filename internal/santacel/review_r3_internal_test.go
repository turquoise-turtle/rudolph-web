package santacel

// Review of the input handling fixes, round 3: the try form's guards are bound to the overload IDs try.go lists, and
// cel-go keeps its own implementation for any overload not listed. A cel-go upgrade that adds an overload to replace,
// format, join, split, matches, _+_, the strings extension or the conversions would bypass the guards without a word.
// This test enumerates the overloads the environment declares and needs each that builds a string, splits one, or
// compiles a regex to be refused by the try form, so a new overload fails here until someone adds a case for it.

import (
	"strings"
	"testing"
)

// r3GuardedNames are the functions whose calls can build strings, lists of them, or regex programs.
var r3GuardedNames = []string{
	"_+_", "replace", "format", "join", "split", "matches",
	"charAt", "indexOf", "lastIndexOf", "lowerAscii", "upperAscii", "reverse", "substring", "trim",
	"bytes", "string",
}

// r3UnguardedOverloads build nothing in proportion to their arguments' length (numbers, times, a trimmed slice of the
// receiver, a value's short string), so they need no guard. add_list builds a list rather than a string, which the
// cost limit charges per element.
var r3UnguardedOverloads = map[string]bool{
	"add_double": true, "add_duration_duration": true, "add_duration_timestamp": true, "add_timestamp_duration": true,
	"add_int64": true, "add_uint64": true, "add_list": true,
	"string_trim":      true,
	"bytes_to_bytes":   true,
	"string_to_string": true, "bool_to_string": true, "double_to_string": true, "duration_to_string": true,
	"int64_to_string": true, "timestamp_to_string": true, "uint64_to_string": true,
}

// r3Attack calls one guarded overload with arguments that build more than the try form allows in all, through that
// overload alone: the other calls in each build little, or build 3.75 MiB that only the overload's own charge takes
// past the 4 MiB bound. A loop goes on after a call is refused and cel-go charges the refused calls too, so each
// loop, over the working directory's cwd characters, passes the bound well within cel-go's cost limit, whose error
// would otherwise replace the refusal.
type r3Attack struct {
	expr string
	cwd  int
}

// r3AfterConcat builds 3.75 MiB by 240 concatenations of the 8 KiB path, then makes up to 20 calls of call.
func r3AfterConcat(call string) r3Attack {
	return r3Attack{"cwd.split('').all(c, (path + path).size() > 0) && [" + strings.Repeat("0,", 19) + "0].all(i, " + call + ")", 240}
}

var r3OverloadAttacks = map[string]r3Attack{
	"add_string":                       {"cwd.split('').all(c, (path + path).size() > 0)", 300},
	"add_bytes":                        {"[bytes(path)].all(b, cwd.split('').all(c, (b + b).size() > 0))", 300},
	"string_replace_string_string":     {"path.replace('a', path) == ''", 0},
	"string_replace_string_string_int": {"path.replace('a', path, -1) == ''", 0},
	"string_format":                    {"'%s'.format([path.split('').map(c, path)]) == ''", 0},
	"list_join":                        {"path.split('').map(c, path).join() == ''", 0},
	"list_join_string":                 {"path.split('').map(c, path).join(',') == ''", 0},
	"string_split_string":              {"cwd.split('').all(c, path.split('').size() > 0)", 50},
	"string_split_string_int":          {"cwd.split('').all(c, path.split('', -1).size() > 0)", 50},
	"matches":                          {"matches('a', '" + strings.Repeat("a{1000}", 3) + "')", 0},
	"matches_string":                   {"'a'.matches('" + strings.Repeat("a{1000}", 3) + "')", 0},
	"string_char_at_int":               r3AfterConcat("path.charAt(0) != ''"),
	"string_index_of_string":           r3AfterConcat("path.indexOf('b') < 0"),
	"string_index_of_string_int":       r3AfterConcat("path.indexOf('b', 0) < 0"),
	"string_last_index_of_string":      r3AfterConcat("path.lastIndexOf('b') < 0"),
	"string_last_index_of_string_int":  r3AfterConcat("path.lastIndexOf('b', 0) < 0"),
	"string_lower_ascii":               r3AfterConcat("path.lowerAscii() != ''"),
	"string_upper_ascii":               r3AfterConcat("path.upperAscii() != ''"),
	"string_reverse":                   r3AfterConcat("path.reverse() != ''"),
	"string_substring_int":             r3AfterConcat("path.substring(1) != ''"),
	"string_substring_int_int":         r3AfterConcat("path.substring(1, 8000) != ''"),
	"string_to_bytes":                  {"cwd.split('').all(c, bytes(path).size() > 0)", 600},
	"bytes_to_string":                  {"[bytes(path)].all(b, cwd.split('').all(c, string(b).size() > 0))", 600},
}

func TestEvaluate_ReviewR3_EveryGuardedOverloadIsGuarded(t *testing.T) {
	declared := map[string]bool{}
	for _, name := range r3GuardedNames {
		fn, ok := env.Functions()[name]
		if !ok {
			t.Errorf("the CEL environment no longer declares %s; update this test", name)
			continue
		}
		for _, o := range fn.OverloadDecls() {
			id := o.ID()
			declared[id] = true
			if r3UnguardedOverloads[id] {
				continue
			}
			attack, ok := r3OverloadAttacks[id]
			if !ok {
				t.Errorf("%s has an overload %s this test does not know: give it a case here, and make sure try.go guards it", name, id)
				continue
			}
			t.Run(id, func(t *testing.T) {
				_, err := Evaluate(attack.expr, Context{Path: strings.Repeat("a", 8192), CWD: strings.Repeat("c", attack.cwd)})
				if err == nil || !strings.Contains(err.Error(), "the try form") {
					t.Errorf("%.80q: got error %v; want the try form's refusal, from the guard on %s", attack.expr, err, id)
				}
			})
		}
	}
	for id := range r3OverloadAttacks {
		if !declared[id] {
			t.Errorf("the CEL environment no longer declares %s; remove its case", id)
		}
	}
}
