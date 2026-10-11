package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// This file is the two things that stand between a model's tool call and the tool
// itself: a check against the schema the model was shown, and a breaker for a call
// it keeps repeating after it failed.
//
// The validator implements exactly the JSON Schema keywords the built-in schemas
// use — counted by a command rather than guessed (issue #200):
//
//	grep -rhoE '"(type|required|enum|items|properties|pattern|additionalProperties|
//	  minimum|maximum|minItems|oneOf|anyOf|format)"' --include='*.go' \
//	  --exclude='*_test.go' tool/ | sort | uniq -c | sort -rn
//	# 65 "type"  24 "properties"  15 "required"  5 "enum"  3 "items"  1 "additionalProperties"
//
// The single additionalProperties is true, which is the default, so there is
// nothing to enforce. Everything else is ignored on purpose: refusing an argument
// over a rule this validator cannot evaluate would be worse than not checking it.

// validateTypes is the JSON type vocabulary, so a message can name what arrived.
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, float32, int, int64, int32:
		return "number"
	case []any, []string, []map[string]any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// ValidateArgs checks one tool call's arguments against its schema and returns one
// message per problem, in schema order.
//
// Every problem is returned rather than the first: a model can fix three mistakes
// in one step, but not three steps' worth of one mistake each. A nil or empty
// schema says nothing, so nothing is rejected.
func ValidateArgs(schema map[string]any, args map[string]any) []string {
	if len(schema) == 0 {
		return nil
	}
	var problems []string
	validateValue(schema, args, "", &problems)
	return problems
}

// validateValue checks one value against one schema node. path is where the value
// sits inside the arguments, for the message: "" at the top, "todos[1].status"
// deeper in.
func validateValue(schema map[string]any, value any, path string, problems *[]string) {
	if len(schema) == 0 {
		return
	}

	if want, ok := schema["type"]; ok && !matchesType(want, value) {
		*problems = append(*problems, fmt.Sprintf("%s must be %s, got %s",
			describePath(path), typeList(want), jsonTypeName(value)))
		// The remaining checks describe a value of the expected shape; there is no
		// point reporting an enum mismatch for something that is not even the right
		// kind of thing.
		return
	}

	if enum, ok := schema["enum"]; ok {
		if !inEnum(enum, value) {
			*problems = append(*problems, fmt.Sprintf("%s must be one of %s, got %s",
				describePath(path), enumList(enum), quoteValue(value)))
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		validateObject(schema, typed, path, problems)
	case []any:
		validateItems(schema, typed, path, problems)
	}
}

// validateObject applies required and properties to an object value.
func validateObject(schema map[string]any, value map[string]any, path string, problems *[]string) {
	if required, ok := schema["required"]; ok {
		for _, name := range stringList(required) {
			if _, present := value[name]; !present {
				*problems = append(*problems, fmt.Sprintf("missing required property %s",
					quoted(childPath(path, name))))
			}
		}
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	// Schema order, so two runs report the same problems in the same order.
	for _, name := range sortedKeys(properties) {
		child, present := value[name]
		if !present {
			continue
		}
		node, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		validateValue(node, child, childPath(path, name), problems)
	}
}

// validateItems applies the element schema to each element of an array.
func validateItems(schema map[string]any, value []any, path string, problems *[]string) {
	items, ok := schema["items"].(map[string]any)
	if !ok {
		return
	}
	for i, element := range value {
		validateValue(items, element, fmt.Sprintf("%s[%d]", path, i), problems)
	}
}

// matchesType reports whether a value satisfies a "type" keyword, which may name
// one type or a list of them.
func matchesType(want any, value any) bool {
	switch names := want.(type) {
	case string:
		return matchesOneType(names, value)
	case []any:
		for _, name := range names {
			if s, ok := name.(string); ok && matchesOneType(s, value) {
				return true
			}
		}
		return false
	case []string:
		for _, name := range names {
			if matchesOneType(name, value) {
				return true
			}
		}
		return false
	default:
		// A form this validator does not understand constrains nothing.
		return true
	}
}

func matchesOneType(name string, value any) bool {
	switch name {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		switch value.(type) {
		case []any, []string, []map[string]any:
			return true
		}
		return false
	case "number":
		return isNumber(value)
	case "integer":
		return isInteger(value)
	case "null":
		return value == nil
	default:
		// An unknown type name constrains nothing.
		return true
	}
}

func isNumber(value any) bool {
	switch value.(type) {
	case float64, float32, int, int64, int32:
		return true
	}
	return false
}

// isInteger accepts a whole float, because JSON has one number type and the
// decoder produces float64 for every one of them.
func isInteger(value any) bool {
	switch n := value.(type) {
	case int, int64, int32:
		return true
	case float64:
		return n == float64(int64(n))
	case float32:
		return n == float32(int64(n))
	}
	return false
}

// inEnum compares by the value's printable form, so a schema that lists numbers or
// booleans works as well as one that lists strings.
func inEnum(enum any, value any) bool {
	for _, candidate := range stringList(enum) {
		if candidate == renderedValue(value) {
			return true
		}
	}
	return false
}

func renderedValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func quoteValue(value any) string {
	if s, ok := value.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(value)
}

// stringList reads a list of strings from either spelling a schema may use: the Go
// literal the built-in tools are written with, or the []any a schema parsed from
// JSON (an MCP tool's) produces.
func stringList(value any) []string {
	switch list := value.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprint(item))
			}
		}
		return out
	default:
		return nil
	}
}

func typeList(want any) string {
	switch names := want.(type) {
	case string:
		return names
	case []any, []string:
		return strings.Join(stringList(names), " or ")
	default:
		return fmt.Sprint(want)
	}
}

func enumList(enum any) string {
	quoted := make([]string, 0)
	for _, item := range stringList(enum) {
		quoted = append(quoted, fmt.Sprintf("%q", item))
	}
	return strings.Join(quoted, ", ")
}

// describePath names a place inside the arguments for a message: "the arguments"
// at the top, and a quoted path below it.
func describePath(path string) string {
	if path == "" {
		return "the arguments"
	}
	return quoted(path)
}

func childPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// quoted wraps a path for a message. The whole path is quoted as one unit, so a
// nested property reads as "todos[1].status" rather than the "todos"[1]."status"
// that quoting each segment separately would produce.
func quoted(path string) string {
	return fmt.Sprintf("%q", path)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// toolCallKey is the identity of one call: its tool and its arguments, with the
// arguments re-marshalled so two spellings of the same object — key order,
// whitespace — are recognised as the same call. A map marshals with sorted keys,
// so this is deterministic.
func toolCallKey(name string, args map[string]any) string {
	canonical, err := json.Marshal(args)
	if err != nil {
		return name
	}
	return name + "\x00" + string(canonical)
}

// rawCallKey is the identity of a call whose arguments did not parse: there is no
// object to canonicalise, so the raw text stands for it.
func rawCallKey(name, raw string) string {
	return name + "\x00raw:" + raw
}

// InvalidArgsMessage is what a model is told when its call does not match the
// schema: every problem, on one line, prefixed with the tool it belongs to.
func InvalidArgsMessage(name string, problems []string) string {
	return fmt.Sprintf("invalid arguments for %s: %s", name, strings.Join(problems, "; "))
}

// repeatFailureLimit is how many times one exact call may fail before it is
// refused. A package variable so a test asserts a counted refusal instead of
// driving that many real failures.
var repeatFailureLimit = 3

// noteCallFailure counts one failed call, keyed by identity.
func (a *Agent) noteCallFailure(key string) {
	if a.runFailures == nil {
		a.runFailures = map[string]int{}
	}
	a.runFailures[key]++
}

// callRefused reports whether this exact call has already failed enough times in
// this run to be refused rather than tried again.
//
// Only failures are counted: asking for the same successful call twice is normal
// work (polling a build, re-reading a file that changed), while asking for the same
// failing call is a loop, and the step budget is what it burns. The count is per
// run, because a fresh prompt is a fresh start and the model deserves one.
func (a *Agent) callRefused(key string) bool {
	return a.runFailures[key] >= repeatFailureLimit
}

// repeatMessage names the call and what to do about it.
func repeatMessage(name string, failures int) string {
	return fmt.Sprintf("[LOOP] %s has already been called with exactly these arguments %d times and failed every time. "+
		"Change the arguments or the approach instead of repeating it.", name, failures)
}
