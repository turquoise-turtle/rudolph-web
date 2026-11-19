package santacel

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/functions"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/parser/gen"
	"github.com/antlr4-go/antlr/v4"
)

// The try form evaluates an expression for anyone signed in, in a Lambda of a
// few hundred MiB. cel-go charges a call against the cost limit only once it
// has run, so a single call can build far more than the limit suggests:
// path.replace('a', path) on an 8 KiB path builds 64 MiB, and a regex of a
// few thousand a{1000} compiles to hundreds of MiB. Each call that builds a
// string, splits one, or compiles or matches a regex therefore works out what
// it is about to build from its actual arguments, and a try is refused only
// when that passes one of these bounds. Real Santa expressions build a few
// KiB at most.
const (
	// maxTryString bounds one string a try builds.
	maxTryString = 1 << 20
	// maxTryBuilt bounds what a try builds in all: strings, their parts, the
	// rune copies the strings extension makes, and regex programs.
	maxTryBuilt = 4 << 20
	// maxTryFormatClauses bounds the clauses of one format string, each of
	// which allocates on its own.
	maxTryFormatClauses = 10_000
	// maxTryPattern bounds a regex's length, since parsing it allocates in
	// proportion; a literal one is at most the 16 KiB of the expression.
	maxTryPattern = 16 << 10
	// maxTryRegexInstructions bounds one regex's compiled program. Real
	// patterns compile to tens or hundreds of instructions.
	maxTryRegexInstructions = 2_000
	// regexInstructionBytes is about what Go allocates per instruction to
	// compile a program and match with it.
	regexInstructionBytes = 256
	// maxTryRegexSteps bounds the matching a try does in all, counted as
	// instructions times characters: Go's matcher takes up to that many steps,
	// and 10 million take well under a second.
	maxTryRegexSteps = 10_000_000
	// maxTryNegativeNumbers bounds the negative numbers in a tried expression.
	// cel-go's parser looks far ahead to tell a minus sign on a number from
	// one on an expression, about 40 KiB each, so 3,270 of them allocate
	// 130 MiB before anything runs. Real expressions have a handful at most.
	maxTryNegativeNumbers = 128
)

var (
	errTryString        = fmt.Errorf("the expression builds a string longer than the %d MiB the try form allows", maxTryString>>20)
	errTryBuilt         = fmt.Errorf("the expression builds more than the %d MiB of strings the try form allows in all", maxTryBuilt>>20)
	errTryClauses       = fmt.Errorf("a format string has more than %s clauses, more than the try form formats", thousands(maxTryFormatClauses))
	errTryPattern       = fmt.Errorf("a regular expression is longer than the %d KiB the try form compiles", maxTryPattern>>10)
	errTryRegex         = fmt.Errorf("a regular expression is too large for the try form: it compiles to more than %s instructions", thousands(maxTryRegexInstructions))
	errTryRegexSteps    = errors.New("matching the expression's regular expressions would take longer than the try form allows")
	errTryNegativeCount = fmt.Errorf("the expression has more than %d negative numbers, more than the try form parses", maxTryNegativeNumbers)
)

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// tryGuard is what one try has built so far.
type tryGuard struct {
	built   uint64
	steps   uint64
	regexps map[string]tryRegex
}

type tryRegex struct {
	re           *regexp.Regexp
	instructions uint64
	err          error
}

func (g *tryGuard) charge(n uint64) error {
	g.built += n
	if g.built > maxTryBuilt {
		return errTryBuilt
	}
	return nil
}

// buildString charges for a string of n bytes, refusing it past maxTryString.
func (g *tryGuard) buildString(n uint64) error {
	if n > maxTryString {
		return errTryString
	}
	return g.charge(n)
}

// guardedFunction checks a call before cel-go's own implementation runs it,
// returning why it would build too much, or nil. Arguments of types it does
// not handle are left to that implementation, which refuses them.
type guardedFunction struct {
	name  string
	ids   []string
	check func(g *tryGuard, args []ref.Val) error
	// run, when set, replaces cel-go's implementation, orig, for arguments
	// check passes, or returns nil to leave them to it.
	run func(g *tryGuard, orig *functions.Overload, args []ref.Val) ref.Val
}

var guardedFunctions = []guardedFunction{
	{name: "_+_", ids: []string{"add_string", "add_bytes"}, check: checkConcat},
	{name: "replace", ids: []string{"string_replace_string_string", "string_replace_string_string_int"}, check: checkReplace},
	{name: "format", ids: []string{"string_format"}, check: checkFormat},
	{name: "join", ids: []string{"list_join", "list_join_string"}, check: checkJoin},
	{name: "split", ids: []string{"string_split_string", "string_split_string_int"}, check: checkSplit},
	// The strings extension copies its receiver to runes, four bytes each,
	// and these but charAt and indexOf build a string of that length again.
	{name: "charAt", ids: []string{"string_char_at_int"}, check: runeCopies(4)},
	{name: "indexOf", ids: []string{"string_index_of_string", "string_index_of_string_int"}, check: runeCopies(4)},
	{name: "lastIndexOf", ids: []string{"string_last_index_of_string", "string_last_index_of_string_int"}, check: runeCopies(4)},
	{name: "lowerAscii", ids: []string{"string_lower_ascii"}, check: runeCopies(5)},
	{name: "upperAscii", ids: []string{"string_upper_ascii"}, check: runeCopies(5)},
	{name: "reverse", ids: []string{"string_reverse"}, check: runeCopies(5)},
	{name: "substring", ids: []string{"string_substring_int", "string_substring_int_int"}, check: runeCopies(5)},
	{name: "bytes", ids: []string{"string_to_bytes"}, check: checkConversion},
	{name: "string", ids: []string{"bytes_to_string"}, check: checkConversion},
	{name: "matches", ids: []string{"matches", "matches_string"}, check: checkMatches, run: runMatches},
	// A failed conversion's error quotes the string it could not convert.
	{name: "int", ids: []string{"string_to_int64"}, check: checkBuilt, run: runConversion},
	{name: "uint", ids: []string{"string_to_uint64"}, check: checkBuilt, run: runConversion},
	{name: "double", ids: []string{"string_to_double"}, check: checkBuilt, run: runConversion},
	{name: "timestamp", ids: []string{"string_to_timestamp"}, check: checkBuilt, run: runConversion},
	{name: "duration", ids: []string{"string_to_duration"}, check: checkBuilt, run: runConversion},
	// cel-go loads a named time zone from the zone files on every call, and
	// charges the call as one.
	{name: "getFullYear", ids: []string{"timestamp_to_year_with_tz"}, check: checkZone},
	{name: "getMonth", ids: []string{"timestamp_to_month_with_tz"}, check: checkZone},
	{name: "getDayOfYear", ids: []string{"timestamp_to_day_of_year_with_tz"}, check: checkZone},
	{name: "getDayOfMonth", ids: []string{"timestamp_to_day_of_month_with_tz"}, check: checkZone},
	{name: "getDate", ids: []string{"timestamp_to_day_of_month_1_based_with_tz"}, check: checkZone},
	{name: "getDayOfWeek", ids: []string{"timestamp_to_day_of_week_with_tz"}, check: checkZone},
	{name: "getHours", ids: []string{"timestamp_to_hours_with_tz"}, check: checkZone},
	{name: "getMinutes", ids: []string{"timestamp_to_minutes_with_tz"}, check: checkZone},
	{name: "getSeconds", ids: []string{"timestamp_to_seconds_tz"}, check: checkZone},
	{name: "getMilliseconds", ids: []string{"timestamp_to_milliseconds_with_tz"}, check: checkZone},
}

// originalBindings are cel-go's implementations of the guarded functions, by
// function name and by overload ID.
var originalBindings map[string]*functions.Overload

func init() {
	originalBindings = map[string]*functions.Overload{}
	decls := env.Functions()
	for _, f := range guardedFunctions {
		fn, ok := decls[f.name]
		if !ok {
			panic("santacel: the CEL environment has no function " + f.name)
		}
		declared := map[string]bool{}
		for _, o := range fn.OverloadDecls() {
			declared[o.ID()] = true
		}
		for _, id := range f.ids {
			if !declared[id] {
				panic("santacel: the CEL environment has no overload " + id + " of " + f.name)
			}
		}
		bindings, err := fn.Bindings()
		if err != nil {
			panic(fmt.Sprintf("santacel: the bindings of %s: %v", f.name, err))
		}
		for _, b := range bindings {
			originalBindings[b.Operator] = b
		}
		if originalBindings[f.name] == nil {
			panic("santacel: no binding for " + f.name)
		}
	}
}

// programOption puts the guarded functions in place of cel-go's for one try.
// The program has to be given them: cel.Function cannot replace a function
// bound once for all its overloads, such as _+_ and matches. Each is bound by
// function name, for calls whose overload is chosen at run time, and by each
// overload ID, for calls the checker resolved.
func (g *tryGuard) programOption() cel.ProgramOption {
	var overloads []*functions.Overload
	for _, f := range guardedFunctions {
		ops := append([]string{f.name}, f.ids...)
		slices.Sort(ops)
		for _, op := range slices.Compact(ops) {
			orig := originalBindings[op]
			if orig == nil {
				orig = originalBindings[f.name]
			}
			overloads = append(overloads, &functions.Overload{
				Operator:     op,
				OperandTrait: orig.OperandTrait,
				NonStrict:    orig.NonStrict,
				Function: func(args ...ref.Val) ref.Val {
					if f.check != nil {
						if err := f.check(g, args); err != nil {
							return types.WrapErr(err)
						}
					}
					if f.run != nil {
						if v := f.run(g, orig, args); v != nil {
							return v
						}
					}
					return callOriginal(orig, args)
				},
			})
		}
	}
	return cel.Functions(overloads...)
}

func callOriginal(o *functions.Overload, args []ref.Val) ref.Val {
	switch {
	case len(args) == 1 && o.Unary != nil:
		return o.Unary(args[0])
	case len(args) == 2 && o.Binary != nil:
		return o.Binary(args[0], args[1])
	case o.Function != nil:
		return o.Function(args...)
	}
	return types.NoSuchOverloadErr()
}

func strArg(args []ref.Val, i int) (string, bool) {
	if i >= len(args) {
		return "", false
	}
	s, ok := args[i].(types.String)
	return string(s), ok
}

func checkConcat(g *tryGuard, args []ref.Val) error {
	if len(args) != 2 {
		return nil
	}
	switch a := args[0].(type) {
	case types.String:
		if b, ok := args[1].(types.String); ok {
			return g.buildString(uint64(len(a)) + uint64(len(b)))
		}
	case types.Bytes:
		if b, ok := args[1].(types.Bytes); ok {
			return g.buildString(uint64(len(a)) + uint64(len(b)))
		}
	}
	return nil
}

// checkReplace counts the replacements first, as strings.Replace does, to
// know the result's length.
func checkReplace(g *tryGuard, args []ref.Val) error {
	s, ok1 := strArg(args, 0)
	old, ok2 := strArg(args, 1)
	repl, ok3 := strArg(args, 2)
	if !ok1 || !ok2 || !ok3 {
		return nil
	}
	n := strings.Count(s, old)
	if len(args) == 4 {
		limit, ok := args[3].(types.Int)
		if !ok {
			return nil
		}
		if limit >= 0 && int64(limit) < int64(n) {
			n = int(limit)
		}
	}
	size := uint64(len(s)) - uint64(n)*uint64(len(old)) + uint64(n)*uint64(len(repl))
	return g.buildString(size)
}

func checkJoin(g *tryGuard, args []ref.Val) error {
	if len(args) == 0 {
		return nil
	}
	list, ok := args[0].(traits.Lister)
	if !ok {
		return nil
	}
	var sep string
	if len(args) == 2 {
		if sep, ok = strArg(args, 1); !ok {
			return nil
		}
	}
	var size uint64
	for i := types.Int(0); i < list.Size().(types.Int); i++ {
		s, ok := list.Get(i).(types.String)
		if !ok {
			return nil
		}
		if i > 0 {
			size += uint64(len(sep))
		}
		if size += uint64(len(s)); size > maxTryString {
			return errTryString
		}
	}
	return g.buildString(size)
}

// stringHeaderBytes is what a part of a split costs: it shares its bytes with
// the string split, but the list holds its header.
const stringHeaderBytes = 16

func checkSplit(g *tryGuard, args []ref.Val) error {
	s, ok1 := strArg(args, 0)
	sep, ok2 := strArg(args, 1)
	if !ok1 || !ok2 {
		return nil
	}
	parts := strings.Count(s, sep) + 1
	if sep == "" {
		parts = utf8.RuneCountInString(s)
	}
	if len(args) == 3 {
		limit, ok := args[2].(types.Int)
		if !ok {
			return nil
		}
		if limit >= 0 && int64(limit) < int64(parts) {
			parts = int(limit)
		}
	}
	return g.charge(uint64(parts) * stringHeaderBytes)
}

// runeCopies charges factor bytes per byte of the receiver and of a string
// argument.
func runeCopies(factor uint64) func(*tryGuard, []ref.Val) error {
	return func(g *tryGuard, args []ref.Val) error {
		var n uint64
		for _, a := range args {
			if s, ok := a.(types.String); ok {
				n += uint64(len(s))
			}
		}
		return g.charge(factor * n)
	}
}

func checkConversion(g *tryGuard, args []ref.Val) error {
	if len(args) != 1 {
		return nil
	}
	switch v := args[0].(type) {
	case types.String:
		return g.charge(uint64(len(v)))
	case types.Bytes:
		return g.charge(uint64(len(v)))
	}
	return nil
}

func checkFormat(g *tryGuard, args []ref.Val) error {
	f, ok := strArg(args, 0)
	if !ok || len(args) != 2 {
		return nil
	}
	list, ok := args[1].(traits.Lister)
	if !ok {
		return nil
	}
	size, copied, err := formatSize(f, list)
	if err != nil {
		return err
	}
	if err := g.buildString(size); err != nil {
		return err
	}
	return g.charge((formatOverhead - 1) * (size + copied))
}

// formatOverhead is about how many bytes format allocates per byte it writes:
// each clause is written to a string of its own, then to a builder that grows
// by doubling.
const formatOverhead = 4

// formatSize is the length of f.format(list) as the strings extension (v4 and
// later) writes it, worked out without writing it, and how much of that it
// writes more than once: each key and value of a map is written to a string
// of its own and then copied into the map's, so a value nested in maps is
// written again at every level. Where the clauses stop making sense, it stops
// counting: format then refuses them itself, having written no more than
// counted.
func formatSize(f string, list traits.Lister) (n, copied uint64, err error) {
	var (
		clauses int
		arg     int64
		scratch = make([]byte, 0, 512)
	)
	count := int64(list.Size().(types.Int))
	for i := 0; i < len(f); {
		if f[i] != '%' {
			n++
			i++
			continue
		}
		if i+1 < len(f) && f[i+1] == '%' {
			n++
			i += 2
			continue
		}
		if clauses++; clauses > maxTryFormatClauses {
			return n, copied, errTryClauses
		}
		i++
		precision := 6
		if i < len(f) && f[i] == '.' {
			j := i + 1
			for j < len(f) && f[j] >= '0' && f[j] <= '9' {
				j++
			}
			p, err := strconv.Atoi(f[i+1 : j])
			if err != nil || p > 100 {
				return n, copied, nil
			}
			precision, i = p, j
		}
		if i >= len(f) || arg >= count {
			return n, copied, nil
		}
		m, ok := clauseSize(f[i], precision, list.Get(types.Int(arg)), maxTryString-n, &scratch, &copied)
		if !ok {
			return n, copied, nil
		}
		if n += m; n > maxTryString {
			return n, copied, errTryString
		}
		if copied > maxTryBuilt {
			return n, copied, errTryBuilt
		}
		arg++
		i++
	}
	return n, copied, nil
}

func clauseSize(verb byte, precision int, v ref.Val, room uint64, scratch *[]byte, copied *uint64) (uint64, bool) {
	appended := func(b []byte) (uint64, bool) {
		*scratch = b[:0]
		return uint64(len(b)), true
	}
	float := func(v ref.Val) (float64, bool) {
		switch x := v.(type) {
		case types.Int:
			return float64(x), true
		case types.Uint:
			return float64(x), true
		case types.Double:
			return float64(x), true
		}
		return 0, false
	}
	switch verb {
	case 's':
		return valueSize(v, room, scratch, copied)
	case 'd':
		switch v.(type) {
		case types.Int, types.Uint, types.Double:
			return valueSize(v, room, scratch, copied)
		}
	case 'f', 'e':
		x, ok := float(v)
		if !ok {
			return 0, false
		}
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 9, true
		}
		return appended(strconv.AppendFloat(*scratch, x, verb, precision, 64))
	case 'b', 'o', 'x', 'X':
		base := map[byte]int{'b': 2, 'o': 8, 'x': 16, 'X': 16}[verb]
		switch x := v.(type) {
		case types.Bool:
			if verb == 'b' {
				return 1, true
			}
		case types.Int:
			return appended(strconv.AppendInt(*scratch, int64(x), base))
		case types.Uint:
			return appended(strconv.AppendUint(*scratch, uint64(x), base))
		case types.String:
			if base == 16 {
				return 2 * uint64(len(x)), true
			}
		case types.Bytes:
			if base == 16 {
				return 2 * uint64(len(x)), true
			}
		}
	}
	return 0, false
}

// valueSize is the length of v written by %s, counting no further than room,
// and adds to copied what it writes twice: the keys and values of maps.
func valueSize(v ref.Val, room uint64, scratch *[]byte, copied *uint64) (uint64, bool) {
	appended := func(b []byte) uint64 {
		*scratch = b[:0]
		return uint64(len(b))
	}
	switch v.Type() {
	case types.BoolType:
		if v == types.True {
			return 4, true
		}
		return 5, true
	case types.IntType:
		x, ok := v.Value().(int64)
		return appended(strconv.AppendInt(*scratch, x, 10)), ok
	case types.UintType:
		x, ok := v.Value().(uint64)
		return appended(strconv.AppendUint(*scratch, x, 10)), ok
	case types.DoubleType:
		x, ok := v.Value().(float64)
		if !ok {
			return 0, false
		}
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 9, true
		}
		return appended(strconv.AppendFloat(*scratch, x, 'f', -1, 64)), true
	case types.BytesType:
		x, ok := v.Value().([]byte)
		return uint64(len(x)), ok
	case types.StringType:
		x, ok := v.Value().(string)
		return uint64(len(x)), ok
	case types.DurationType:
		x, ok := v.Value().(time.Duration)
		return appended(strconv.AppendFloat(*scratch, x.Seconds(), 'f', -1, 64)) + 1, ok
	case types.TimestampType:
		x, ok := v.Value().(time.Time)
		return appended(x.UTC().AppendFormat(*scratch, time.RFC3339Nano)), ok
	case types.NullType:
		return 4, true
	case types.TypeType:
		x, ok := v.Value().(string)
		return uint64(len(x)), ok
	case types.ListType:
		list, ok := v.(traits.Lister)
		if !ok {
			return 0, false
		}
		n := uint64(2)
		for it, i := list.Iterator(), 0; it.HasNext() == types.True && n <= room; i++ {
			if i > 0 {
				n += 2
			}
			m, ok := valueSize(it.Next(), room-min(n, room), scratch, copied)
			if !ok {
				return n, false
			}
			n += m
		}
		return n, true
	case types.MapType:
		m, ok := v.(traits.Mapper)
		if !ok {
			return 0, false
		}
		n := uint64(2)
		for it, i := m.Iterator(), 0; it.HasNext() == types.True && n <= room; i++ {
			if i > 0 {
				n += 2
			}
			key := it.Next()
			val, found := m.Find(key)
			if !found {
				return n, false
			}
			k, ok1 := valueSize(key, room-min(n, room), scratch, copied)
			e, ok2 := valueSize(val, room-min(n+k, room), scratch, copied)
			if !ok1 || !ok2 {
				return n, false
			}
			n += k + 2 + e
			*copied += k + e
		}
		return n, true
	}
	return 0, false
}

// checkMatches refuses a regex too long or too large to compile, or a match
// that would take too long, before compiling or running it.
func checkMatches(g *tryGuard, args []ref.Val) error {
	s, ok1 := strArg(args, 0)
	pattern, ok2 := strArg(args, 1)
	if !ok1 || !ok2 {
		return nil
	}
	r, err := g.regex(pattern)
	if err != nil {
		return err
	}
	if r.err != nil {
		return nil
	}
	g.steps += r.instructions * (uint64(len(s)) + 1)
	if g.steps > maxTryRegexSteps {
		return errTryRegexSteps
	}
	return nil
}

// runMatches matches with the program checkMatches compiled, as the standard
// library's matches does with regexp.MatchString, so that a pattern is
// compiled once in a try however often it is matched.
func runMatches(g *tryGuard, _ *functions.Overload, args []ref.Val) ref.Val {
	s, _ := strArg(args, 0)
	pattern, _ := strArg(args, 1)
	r := g.regexps[pattern]
	if r.re == nil {
		return nil
	}
	return types.Bool(r.re.MatchString(s))
}

func (g *tryGuard) regex(pattern string) (tryRegex, error) {
	if r, ok := g.regexps[pattern]; ok {
		return r, nil
	}
	if len(pattern) > maxTryPattern {
		return tryRegex{}, errTryPattern
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		// matches reports the error itself, as it would for any try.
		r := tryRegex{err: err}
		g.remember(pattern, r)
		return r, nil
	}
	n := programSize(parsed)
	if n > maxTryRegexInstructions {
		return tryRegex{}, errTryRegex
	}
	if err := g.charge(n * regexInstructionBytes); err != nil {
		return tryRegex{}, err
	}
	re, err := regexp.Compile(pattern)
	r := tryRegex{re: re, instructions: n, err: err}
	g.remember(pattern, r)
	return r, nil
}

func (g *tryGuard) remember(pattern string, r tryRegex) {
	if g.regexps == nil {
		g.regexps = map[string]tryRegex{}
	}
	g.regexps[pattern] = r
}

// programSize is about how many instructions Go compiles re to, counted on the
// parsed pattern, which keeps a{1000} as one repeat: compiling writes out each
// repeat as that many copies, nested repeats multiplied through. It saturates
// well past any bound rather than overflow.
func programSize(re *syntax.Regexp) uint64 {
	const ceiling = 1 << 40
	add := func(a, b uint64) uint64 { return min(a+b, ceiling) }
	mul := func(a, b uint64) uint64 {
		if a != 0 && b > ceiling/a {
			return ceiling
		}
		return a * b
	}
	switch re.Op {
	case syntax.OpLiteral:
		return uint64(len(re.Rune))
	case syntax.OpConcat, syntax.OpAlternate:
		n := uint64(1)
		for _, sub := range re.Sub {
			n = add(n, add(programSize(sub), 1))
		}
		return n
	case syntax.OpCapture, syntax.OpStar, syntax.OpPlus, syntax.OpQuest:
		return add(programSize(re.Sub[0]), 2)
	case syntax.OpRepeat:
		copies := re.Max
		if copies == -1 {
			copies = re.Min + 1
		}
		return mul(uint64(max(copies, 1)), add(programSize(re.Sub[0]), 2))
	}
	return 1
}

// checkBuilt refuses any call once the try has built more than it allows,
// for calls that charge only after they have run.
func checkBuilt(g *tryGuard, _ []ref.Val) error {
	if g.built > maxTryBuilt {
		return errTryBuilt
	}
	return nil
}

// runConversion charges a failed conversion of a string for what it built:
// the error the parser gave, which copies or quotes the string (quoting takes
// at most four bytes a byte), and cel-go's error. A conversion that succeeds
// builds nothing in proportion to the string.
func runConversion(g *tryGuard, orig *functions.Overload, args []ref.Val) ref.Val {
	v := callOriginal(orig, args)
	if e, ok := v.(*types.Err); ok {
		s, _ := strArg(args, 0)
		if err := g.charge(4*uint64(len(s)) + uint64(len(e.String()))); err != nil {
			return types.WrapErr(err)
		}
	}
	return v
}

// zoneLoadBytes is about what loading a named time zone allocates: 5 KiB for
// Australia/Sydney, 8.5 KiB for Europe/London.
const zoneLoadBytes = 8 << 10

// checkZone charges a call with a named time zone for loading the zone, which
// cel-go does on every call. UTC and Local are not loaded, and an offset such
// as +10:00 is not a name.
func checkZone(g *tryGuard, args []ref.Val) error {
	name, ok := strArg(args, 1)
	if !ok || name == "" || name == "UTC" || name == "Local" || strings.Contains(name, ":") {
		return nil
	}
	return g.charge(zoneLoadBytes)
}

// negativeNumbers counts the minus signs directly before a number, with
// cel-go's own lexer, so that comments, keywords and unclosed quotes are read
// as the parser will read them. A minus between two operands is counted too:
// the lexer cannot tell it apart, and real expressions have few of either.
func negativeNumbers(expr string) int {
	lexer := &tokenTypes{CELLexer: gen.NewCELLexer(antlr.NewInputStream(expr))}
	lexer.RemoveErrorListeners()
	lexer.Virt = lexer
	n := 0
	minus := false
	for {
		switch lexer.NextToken().GetTokenType() {
		case antlr.TokenEOF:
			return n
		case gen.CELLexerWHITESPACE, gen.CELLexerCOMMENT:
			// The parser does not see them, so they do not part a sign from
			// its number.
		case gen.CELLexerNUM_INT, gen.CELLexerNUM_UINT, gen.CELLexerNUM_FLOAT:
			if minus {
				n++
			}
			minus = false
		case gen.CELLexerMINUS:
			minus = true
		default:
			minus = false
		}
	}
}

// tokenTypes is cel-go's lexer handing out the same token each time, keeping
// only its type: a token of its own each would allocate about 1.5 MiB for
// 16 KiB of short tokens, on top of what parsing them costs.
type tokenTypes struct {
	*gen.CELLexer
	token reusedToken
}

func (l *tokenTypes) Emit() antlr.Token {
	l.token.tokenType = l.GetType()
	l.EmitToken(&l.token)
	return &l.token
}

type reusedToken struct {
	antlr.Token
	tokenType int
}

func (t *reusedToken) GetTokenType() int { return t.tokenType }

// cel-go's checker costs far more than parsing for two kinds of expression,
// which the try form refuses before checking. An expression with either
// fails the check or is nothing a rule would be, so this changes only why it
// is refused.
//
// The checker suggests a declared name for each name it cannot find, comparing
// it with every declared name by edit distance, so a long undeclared name, or
// many of them, cost far more to check than to parse.
//
// It works out a type for each list and map, nested as deeply as they are, and
// the work grows with about the fourth power of the depth: lists and maps 64
// deep cost 12 MiB to check, and 140 deep over 200 MiB.
const (
	// maxTryUndeclaredName bounds an undeclared name's length. Every name the
	// environment declares is far shorter.
	maxTryUndeclaredName = 64
	// maxTryUndeclared bounds how many references to undeclared names a try
	// checks.
	maxTryUndeclared = 32
	// maxTryNesting bounds how deeply lists and maps, including those the map
	// and filter macros build, are nested in a try. 16 deep check in about
	// 1 MiB; real expressions nest two or three.
	maxTryNesting = 16
)

var (
	errTryUndeclaredName = fmt.Errorf("the expression uses an undeclared name longer than %d bytes, more than the try form checks", maxTryUndeclaredName)
	errTryUndeclared     = fmt.Errorf("the expression uses undeclared names more than %d times, more than the try form checks", maxTryUndeclared)
	errTryNesting        = fmt.Errorf("the expression nests lists and maps more than %d deep, more than the try form checks", maxTryNesting)
)

var declaredVariables = map[string]bool{}

func init() {
	for _, v := range env.Variables() {
		declaredVariables[v.Name()] = true
	}
}

// declaredIdent reports whether the checker can resolve name as a variable,
// a constant, an enum value or a type.
func declaredIdent(name string) bool {
	if declaredVariables[name] {
		return true
	}
	if _, ok := env.CELTypeProvider().FindIdent(name); ok {
		return true
	}
	_, ok := env.CELTypeProvider().FindStructType(name)
	return ok
}

// qualifiedName is the dotted name a chain of field selections on an
// identifier spells, such as santa.cel.v1.ReturnValue.ALLOWLIST, or false
// for any other expression.
func qualifiedName(e ast.Expr) (string, bool) {
	var fields []string
	for e.Kind() == ast.SelectKind && !e.AsSelect().IsTestOnly() {
		fields = append(fields, e.AsSelect().FieldName())
		e = e.AsSelect().Operand()
	}
	if e.Kind() != ast.IdentKind {
		return "", false
	}
	fields = append(fields, e.AsIdent())
	slices.Reverse(fields)
	return strings.Join(fields, "."), true
}

// maxQualifiedName is longer than any qualified name the environment declares,
// so that a long chain of selections is not looked up at every length.
const maxQualifiedName = 256

// checkParsed refuses a parsed expression with an undeclared name longer than
// maxTryUndeclaredName, more than maxTryUndeclared references to undeclared
// names, or lists and maps nested more than maxTryNesting deep. It resolves
// names as the checker does: a chain of selections may name a qualified
// identifier, and a method call on one a qualified function. Where it is
// unsure, it takes the name as declared.
func checkParsed(root ast.Expr) error {
	undeclared := 0
	refer := func(name string) error {
		if len(name) > maxTryUndeclaredName {
			return errTryUndeclaredName
		}
		if undeclared++; undeclared > maxTryUndeclared {
			return errTryUndeclared
		}
		return nil
	}
	var visit func(e ast.Expr, bound []string, nesting int) error
	visit = func(e ast.Expr, bound []string, nesting int) error {
		if e == nil {
			return nil
		}
		if k := e.Kind(); k == ast.ListKind || k == ast.MapKind {
			if nesting++; nesting > maxTryNesting {
				return errTryNesting
			}
		}
		switch e.Kind() {
		case ast.IdentKind:
			if name := e.AsIdent(); !slices.Contains(bound, name) && !declaredIdent(name) {
				return refer(name)
			}
		case ast.SelectKind:
			name, ok := qualifiedName(e)
			if !ok {
				return visit(e.AsSelect().Operand(), bound, nesting)
			}
			root, _, _ := strings.Cut(name, ".")
			if slices.Contains(bound, root) {
				return nil
			}
			for i := len(name); i > len(root); i = strings.LastIndexByte(name[:i], '.') {
				if i <= maxQualifiedName && declaredIdent(name[:i]) {
					return nil
				}
			}
			if !declaredIdent(root) {
				return refer(root)
			}
		case ast.CallKind:
			call := e.AsCall()
			fn := call.FunctionName()
			if call.IsMemberFunction() {
				target := call.Target()
				if prefix, ok := qualifiedName(target); ok && env.HasFunction(prefix+"."+fn) {
					target = nil
				} else if !env.HasFunction(fn) {
					if err := refer(fn); err != nil {
						return err
					}
				}
				if err := visit(target, bound, nesting); err != nil {
					return err
				}
			} else if !env.HasFunction(fn) {
				if err := refer(fn); err != nil {
					return err
				}
			}
			for _, a := range call.Args() {
				if err := visit(a, bound, nesting); err != nil {
					return err
				}
			}
		case ast.ComprehensionKind:
			c := e.AsComprehension()
			inner := append(slices.Clone(bound), c.IterVar(), c.AccuVar())
			if c.HasIterVar2() {
				inner = append(inner, c.IterVar2())
			}
			for _, sub := range []struct {
				e     ast.Expr
				bound []string
			}{
				{c.IterRange(), bound}, {c.AccuInit(), bound},
				{c.LoopCondition(), inner}, {c.LoopStep(), inner},
				{c.Result(), append(slices.Clone(bound), c.AccuVar())},
			} {
				if err := visit(sub.e, sub.bound, nesting); err != nil {
					return err
				}
			}
		case ast.ListKind:
			for _, el := range e.AsList().Elements() {
				if err := visit(el, bound, nesting); err != nil {
					return err
				}
			}
		case ast.MapKind:
			for _, entry := range e.AsMap().Entries() {
				if err := visit(entry.AsMapEntry().Key(), bound, nesting); err != nil {
					return err
				}
				if err := visit(entry.AsMapEntry().Value(), bound, nesting); err != nil {
					return err
				}
			}
		case ast.StructKind:
			for _, f := range e.AsStruct().Fields() {
				if err := visit(f.AsStructField().Value(), bound, nesting); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(root, nil, 0)
}
