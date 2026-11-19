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
	"cel.dev/cel-go/common/functions"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
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
	// run, when set, replaces cel-go's implementation for arguments check
	// passes.
	run func(g *tryGuard, args []ref.Val) ref.Val
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
					if err := f.check(g, args); err != nil {
						return types.WrapErr(err)
					}
					if f.run != nil {
						if v := f.run(g, args); v != nil {
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
	size, err := formatSize(f, list)
	if err != nil {
		return err
	}
	if err := g.buildString(size); err != nil {
		return err
	}
	return g.charge((formatOverhead - 1) * size)
}

// formatOverhead is about how many bytes format allocates per byte it writes:
// each clause is written to a string of its own, then to a builder that grows
// by doubling.
const formatOverhead = 4

// formatSize is the length of f.format(list) as the strings extension (v4 and
// later) writes it, worked out without writing it. Where the clauses stop
// making sense, it stops counting: format then refuses them itself, having
// written no more than counted.
func formatSize(f string, list traits.Lister) (uint64, error) {
	var (
		n       uint64
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
			return n, errTryClauses
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
				return n, nil
			}
			precision, i = p, j
		}
		if i >= len(f) || arg >= count {
			return n, nil
		}
		m, ok := clauseSize(f[i], precision, list.Get(types.Int(arg)), maxTryString-n, &scratch)
		if !ok {
			return n, nil
		}
		if n += m; n > maxTryString {
			return n, errTryString
		}
		arg++
		i++
	}
	return n, nil
}

func clauseSize(verb byte, precision int, v ref.Val, room uint64, scratch *[]byte) (uint64, bool) {
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
		return valueSize(v, room, scratch)
	case 'd':
		switch v.(type) {
		case types.Int, types.Uint, types.Double:
			return valueSize(v, room, scratch)
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

// valueSize is the length of v written by %s, counting no further than room.
func valueSize(v ref.Val, room uint64, scratch *[]byte) (uint64, bool) {
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
			m, ok := valueSize(it.Next(), room-min(n, room), scratch)
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
			k, ok1 := valueSize(key, room-min(n, room), scratch)
			e, ok2 := valueSize(val, room-min(n+k, room), scratch)
			if !ok1 || !ok2 {
				return n, false
			}
			n += k + 2 + e
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
func runMatches(g *tryGuard, args []ref.Val) ref.Val {
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

// negativeNumbers counts the minus signs written on a number, rather than
// between two operands, outside string literals and comments.
func negativeNumbers(expr string) int {
	n := 0
	var prev byte // the last byte of the last token, or 0 at the start
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		switch {
		case c == '/' && strings.HasPrefix(expr[i:], "//"):
			for i < len(expr) && expr[i] != '\n' {
				i++
			}
		case c == '\'' || c == '"':
			raw := false
			for j := i - 1; j >= 0 && j >= i-2 && strings.IndexByte("rRbB", expr[j]) >= 0; j-- {
				raw = raw || expr[j] == 'r' || expr[j] == 'R'
			}
			quote := expr[i : i+1]
			if strings.HasPrefix(expr[i:], strings.Repeat(quote, 3)) {
				quote = expr[i : i+3]
			}
			i += len(quote)
			for i < len(expr) && !strings.HasPrefix(expr[i:], quote) {
				if expr[i] == '\\' && !raw {
					i++
				}
				i++
			}
			i += len(quote) - 1
			prev = c
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
		case c == '-':
			if prev == 0 || strings.IndexByte("([{,?:!=<>&|+-*/%", prev) >= 0 {
				j := i + 1
				for j < len(expr) && strings.IndexByte(" \t\n\r\f", expr[j]) >= 0 {
					j++
				}
				if j < len(expr) && (isDigit(expr[j]) || expr[j] == '.' && j+1 < len(expr) && isDigit(expr[j+1])) {
					n++
				}
			}
			prev = c
		default:
			prev = c
		}
	}
	return n
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
