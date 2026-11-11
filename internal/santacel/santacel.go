// Package santacel checks and evaluates CEL expressions as Santa does for
// rules from Rudolph. Rudolph syncs with Santa's sync protocol v1, so Santa
// (2026.8) compiles a CEL rule's expression in its CEL v1 environment
// (Source/common/cel/Evaluator.mm with IsV2 false): the santa.cel.v1
// ExecutionContext fields as top-level variables, the ReturnValue names but
// UNSPECIFIED as ints, and cel-cpp's standard library with its strings
// extension. An expression Santa cannot compile is not applied at all, so the
// web view refuses one before it is written.
package santacel

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/ext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Context is a sample execution to evaluate an expression against. A zero
// SigningTime or SecureSigningTime is unset, so has() on it is false, as for
// a binary Santa found no such time for.
type Context struct {
	SigningID         string
	TeamID            string
	PlatformBinary    bool
	SigningTime       time.Time
	SecureSigningTime time.Time
	Args              []string
	Envs              map[string]string
	EUID              int
	CWD               string
	Path              string
}

// returnValues are santa.cel.v1.ReturnValue but UNSPECIFIED, which Santa does
// not declare for v1. 5 to 8 are reserved there: they are v2's TouchID,
// Seatbelt and audit policies.
var returnValues = map[string]int64{
	"ALLOWLIST":            1,
	"ALLOWLIST_COMPILER":   2,
	"BLOCKLIST":            3,
	"SILENT_BLOCKLIST":     4,
	"SILENT_GUI_BLOCKLIST": 9,
	"SILENT_TTY_BLOCKLIST": 10,
}

// Policies are the names an expression can return, in Santa's order.
var Policies = func() []string {
	names := slices.Collect(maps.Keys(returnValues))
	slices.SortFunc(names, func(a, b string) int { return int(returnValues[a] - returnValues[b]) })
	return names
}()

// workshopNames are what Workshop's CEL v2 adds to v1. Santa uses v2 only
// for rules from Workshop, so an expression with any of them fails to compile
// on a Mac syncing with Rudolph.
var (
	workshopVariables = map[string]bool{
		"ancestors": true, "fds": true,
		"REQUIRE_TOUCHID": true, "REQUIRE_TOUCHID_ONLY": true, "SEATBELT": true, "AUDIT": true,
	}
	workshopFunctions = map[string]bool{
		"today": true, "now": true, "days": true, "weekdays": true, "policy_for_range": true, "kill_on_expiry": true,
		"require_touchid_with_cooldown_minutes": true, "require_touchid_only_with_cooldown_minutes": true,
	}
	workshopTargetFields = map[string]bool{"entitlements": true}
)

const workshopFDPrefix = "FD_TYPE_"

// celGoOnlyOverloads are in cel-go's strings extension but not in cel-cpp
// 0.15.0's, which Santa 2026.8 builds with.
var celGoOnlyOverloads = map[string]string{"strings_quote": "strings.quote"}

// contextVariables are the execution context's variables besides target:
// reading any of them makes Santa's result specific to one execution, so it
// is not cached.
var contextVariables = map[string]bool{"args": true, "envs": true, "euid": true, "cwd": true, "path": true}

// Santa compiles with the generated descriptor pool of its santa.cel.v1
// protos; this is the same file built by hand, so the repo needs no protoc.
// Field numbers and names are those of northpolesec/protos 68fc3ca.
var (
	executionContext protoreflect.MessageDescriptor
	executableFile   protoreflect.MessageDescriptor
	env              *cel.Env
)

func init() {
	file, err := protodesc.NewFile(v1File(), protoregistry.GlobalFiles)
	if err != nil {
		panic(fmt.Sprintf("santacel: building santa.cel.v1: %v", err))
	}
	executionContext = file.Messages().ByName("ExecutionContext")
	executableFile = file.Messages().ByName("ExecutableFile")
	opts := []cel.EnvOption{
		cel.TypeDescs(file),
		cel.DeclareContextProto(executionContext),
		ext.Strings(),
	}
	for _, name := range Policies {
		opts = append(opts, cel.Constant(name, cel.IntType, types.Int(returnValues[name])))
	}
	env, err = cel.NewEnv(opts...)
	if err != nil {
		panic(fmt.Sprintf("santacel: building the CEL environment: %v", err))
	}
}

func v1File() *descriptorpb.FileDescriptorProto {
	field := func(name string, number int32, t descriptorpb.FieldDescriptorProto_Type, typeName string, repeated bool) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			JsonName: proto.String(jsonName(name)),
			Number:   proto.Int32(number),
			Type:     t.Enum(),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		if repeated {
			f.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
		}
		return f
	}
	optional := func(f *descriptorpb.FieldDescriptorProto, oneof int32) *descriptorpb.FieldDescriptorProto {
		f.Proto3Optional = proto.Bool(true)
		f.OneofIndex = proto.Int32(oneof)
		return f
	}
	const (
		str     = descriptorpb.FieldDescriptorProto_TYPE_STRING
		msg     = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
		i32     = descriptorpb.FieldDescriptorProto_TYPE_INT32
		boolean = descriptorpb.FieldDescriptorProto_TYPE_BOOL
	)
	enumValue := func(name string, n int32) *descriptorpb.EnumValueDescriptorProto {
		return &descriptorpb.EnumValueDescriptorProto{Name: proto.String(name), Number: proto.Int32(n)}
	}
	returnValue := &descriptorpb.EnumDescriptorProto{
		Name:          proto.String("ReturnValue"),
		Value:         []*descriptorpb.EnumValueDescriptorProto{enumValue("UNSPECIFIED", 0)},
		ReservedRange: []*descriptorpb.EnumDescriptorProto_EnumReservedRange{{Start: proto.Int32(5), End: proto.Int32(8)}},
		ReservedName:  []string{"REQUIRE_TOUCHID", "REQUIRE_TOUCHID_ONLY", "SEATBELT", "AUDIT"},
	}
	for _, name := range Policies {
		returnValue.Value = append(returnValue.Value, enumValue(name, int32(returnValues[name])))
	}
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("santa/cel/v1.proto"),
		Package:    proto.String("santa.cel.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		EnumType:   []*descriptorpb.EnumDescriptorProto{returnValue},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("ExecutionContext"),
				Field: []*descriptorpb.FieldDescriptorProto{
					field("target", 1, msg, ".santa.cel.v1.ExecutableFile", false),
					field("args", 2, str, "", true),
					field("envs", 3, msg, ".santa.cel.v1.ExecutionContext.EnvsEntry", true),
					field("euid", 4, i32, "", false),
					field("cwd", 5, str, "", false),
					field("path", 8, str, "", false),
				},
				NestedType: []*descriptorpb.DescriptorProto{{
					Name: proto.String("EnvsEntry"),
					Field: []*descriptorpb.FieldDescriptorProto{
						field("key", 1, str, "", false),
						field("value", 2, str, "", false),
					},
					Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
				}},
				ReservedRange: []*descriptorpb.DescriptorProto_ReservedRange{{Start: proto.Int32(6), End: proto.Int32(8)}},
			},
			{
				Name: proto.String("ExecutableFile"),
				Field: []*descriptorpb.FieldDescriptorProto{
					optional(field("signing_time", 1, msg, ".google.protobuf.Timestamp", false), 0),
					optional(field("secure_signing_time", 2, msg, ".google.protobuf.Timestamp", false), 1),
					field("signing_id", 3, str, "", false),
					field("is_platform_binary", 5, boolean, "", false),
					field("team_id", 6, str, "", false),
				},
				OneofDecl: []*descriptorpb.OneofDescriptorProto{
					{Name: proto.String("_signing_time")},
					{Name: proto.String("_secure_signing_time")},
				},
				ReservedRange: []*descriptorpb.DescriptorProto_ReservedRange{{Start: proto.Int32(4), End: proto.Int32(5)}},
			},
		},
	}
}

func jsonName(name string) string {
	parts := strings.Split(name, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// Check refuses an expression Santa's CEL v1 would not compile, or whose
// result is not a policy Santa can apply.
func Check(expr string) error {
	_, err := compile(expr)
	return err
}

func compile(expr string) (*cel.Ast, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, errors.New("the expression is empty")
	}
	parsed, iss := env.Parse(expr)
	if iss.Err() != nil {
		return nil, issuesError(iss)
	}
	checked, iss := env.Check(parsed)
	if iss.Err() != nil {
		if names := workshopOnly(parsed.NativeRep().Expr()); len(names) > 0 {
			return nil, fmt.Errorf("%s only in Workshop (North Pole Security's commercial sync server): Santa applies rules from Rudolph with CEL v1, which does not have %s",
				describe(names), plural(names, "it", "them"))
		}
		return nil, issuesError(iss)
	}
	native := checked.NativeRep()
	for _, ref := range native.ReferenceMap() {
		for _, id := range ref.OverloadIDs {
			if name, ok := celGoOnlyOverloads[id]; ok {
				return nil, fmt.Errorf("%s is not in the CEL strings extension Santa has", name)
			}
		}
	}
	switch t := checked.OutputType(); {
	case t.IsExactType(cel.BoolType), t.IsExactType(cel.IntType), t.IsExactType(cel.DynType):
	default:
		return nil, fmt.Errorf("the expression gives a %s; it must return true/false or a policy such as ALLOWLIST or BLOCKLIST", t)
	}
	return checked, nil
}

// issuesError puts cel-go's errors on one line each, with where they are, in
// place of its caret drawing, which needs a monospaced block to make sense.
func issuesError(iss *cel.Issues) error {
	var lines []string
	for _, e := range iss.Errors() {
		msg := e.Message
		if loc := e.Location; loc != nil && loc.Line() > 0 {
			msg = fmt.Sprintf("%s (line %d, column %d)", msg, loc.Line(), loc.Column()+1)
		}
		lines = append(lines, msg)
	}
	if len(lines) == 0 {
		return iss.Err()
	}
	return errors.New(strings.Join(lines, "; "))
}

func describe(names []string) string {
	list := names[0]
	if n := len(names); n > 1 {
		list = strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	}
	return "the expression uses " + list + ", which " + plural(names, "is", "are") + " available"
}

func plural(names []string, one, many string) string {
	if len(names) == 1 {
		return one
	}
	return many
}

// workshopOnly lists the v2 names an expression uses, leaving out
// comprehension variables that happen to share a name.
func workshopOnly(e ast.Expr) []string {
	var found []string
	add := func(name string) {
		if !slices.Contains(found, name) {
			found = append(found, name)
		}
	}
	walk(e, nil, func(e ast.Expr, bound []string) {
		switch e.Kind() {
		case ast.IdentKind:
			name := e.AsIdent()
			if !slices.Contains(bound, name) && (workshopVariables[name] || strings.HasPrefix(name, workshopFDPrefix)) {
				add(name)
			}
		case ast.CallKind:
			if call := e.AsCall(); !call.IsMemberFunction() && workshopFunctions[call.FunctionName()] {
				add(call.FunctionName() + "()")
			}
		case ast.SelectKind:
			sel := e.AsSelect()
			op := sel.Operand()
			if op.Kind() == ast.IdentKind && op.AsIdent() == "target" && !slices.Contains(bound, "target") && workshopTargetFields[sel.FieldName()] {
				add("target." + sel.FieldName())
			}
		}
	})
	return found
}

// walk visits every expression with the comprehension variables in scope.
func walk(e ast.Expr, bound []string, visit func(ast.Expr, []string)) {
	if e == nil {
		return
	}
	visit(e, bound)
	switch e.Kind() {
	case ast.CallKind:
		call := e.AsCall()
		if call.IsMemberFunction() {
			walk(call.Target(), bound, visit)
		}
		for _, a := range call.Args() {
			walk(a, bound, visit)
		}
	case ast.ComprehensionKind:
		c := e.AsComprehension()
		walk(c.IterRange(), bound, visit)
		walk(c.AccuInit(), bound, visit)
		inner := append(slices.Clone(bound), c.IterVar(), c.AccuVar())
		if c.HasIterVar2() {
			inner = append(inner, c.IterVar2())
		}
		walk(c.LoopCondition(), inner, visit)
		walk(c.LoopStep(), inner, visit)
		walk(c.Result(), append(slices.Clone(bound), c.AccuVar()), visit)
	case ast.ListKind:
		for _, el := range e.AsList().Elements() {
			walk(el, bound, visit)
		}
	case ast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			walk(entry.AsMapEntry().Key(), bound, visit)
			walk(entry.AsMapEntry().Value(), bound, visit)
		}
	case ast.SelectKind:
		walk(e.AsSelect().Operand(), bound, visit)
	case ast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			walk(f.AsStructField().Value(), bound, visit)
		}
	}
}

// Cacheable reports whether Santa may cache the expression's result for the
// binary: only when it reads nothing but target, since args, envs, euid, cwd
// and path differ from one execution to the next. Santa decides on what an
// evaluation actually read, so an expression that reads them only on some
// branches is cached when it did not; this answers for every execution.
func Cacheable(expr string) (bool, error) {
	checked, err := compile(expr)
	if err != nil {
		return false, err
	}
	cacheable := true
	walk(checked.NativeRep().Expr(), nil, func(e ast.Expr, bound []string) {
		if e.Kind() == ast.IdentKind && contextVariables[e.AsIdent()] && !slices.Contains(bound, e.AsIdent()) {
			cacheable = false
		}
	})
	return cacheable, nil
}

// costLimit bounds an evaluation from the try form, which anyone signed in
// can use; Santa's own rules are nowhere near it.
const costLimit = 1_000_000

// Evaluate runs an expression against a sample execution and returns the
// policy Santa would apply: true is ALLOWLIST, false BLOCKLIST, and an int
// the policy with that number. UNSPECIFIED, the values reserved for v2 and
// runtime errors are errors, as Santa then applies no rule.
func Evaluate(expr string, c Context) (string, error) {
	checked, err := compile(expr)
	if err != nil {
		return "", err
	}
	prg, err := env.Program(checked, cel.CostLimit(costLimit))
	if err != nil {
		return "", err
	}
	vars, err := cel.ContextProtoVars(c.message())
	if err != nil {
		return "", err
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		return "", err
	}
	switch v := out.(type) {
	case types.Bool:
		if v {
			return "ALLOWLIST", nil
		}
		return "BLOCKLIST", nil
	case types.Int:
		for name, n := range returnValues {
			if n == int64(v) {
				return name, nil
			}
		}
		return "", fmt.Errorf("the expression returned %d, which is not a policy Santa applies to rules from Rudolph", int64(v))
	}
	return "", fmt.Errorf("the expression returned %v (%s); it must return true/false or a policy such as ALLOWLIST", out, out.Type())
}

func (c Context) message() proto.Message {
	target := dynamicpb.NewMessage(executableFile)
	fields := executableFile.Fields()
	target.Set(fields.ByName("signing_id"), protoreflect.ValueOfString(c.SigningID))
	target.Set(fields.ByName("team_id"), protoreflect.ValueOfString(c.TeamID))
	target.Set(fields.ByName("is_platform_binary"), protoreflect.ValueOfBool(c.PlatformBinary))
	if !c.SigningTime.IsZero() {
		target.Set(fields.ByName("signing_time"), protoreflect.ValueOfMessage(timestamppb.New(c.SigningTime).ProtoReflect()))
	}
	if !c.SecureSigningTime.IsZero() {
		target.Set(fields.ByName("secure_signing_time"), protoreflect.ValueOfMessage(timestamppb.New(c.SecureSigningTime).ProtoReflect()))
	}

	m := dynamicpb.NewMessage(executionContext)
	fields = executionContext.Fields()
	m.Set(fields.ByName("target"), protoreflect.ValueOfMessage(target))
	args := m.Mutable(fields.ByName("args")).List()
	for _, a := range c.Args {
		args.Append(protoreflect.ValueOfString(a))
	}
	envs := m.Mutable(fields.ByName("envs")).Map()
	for k, v := range c.Envs {
		envs.Set(protoreflect.ValueOfString(k).MapKey(), protoreflect.ValueOfString(v))
	}
	m.Set(fields.ByName("euid"), protoreflect.ValueOfInt32(int32(c.EUID)))
	m.Set(fields.ByName("cwd"), protoreflect.ValueOfString(c.CWD))
	m.Set(fields.ByName("path"), protoreflect.ValueOfString(c.Path))
	return m
}
