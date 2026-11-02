// Package config holds the dynamic settings tree.
//
// The original Python implementation stores settings as free-form nested
// dicts, and the Web UI relies on round-tripping arbitrary keys
// (e.g. touch.preset_bands, boost.value). To stay wire-compatible we keep the
// tree as map[string]any and provide typed accessors that coerce leniently,
// mirroring Python's float()/int()/bool() semantics.
package config

import (
	"math"
	"reflect"
	"strconv"
	"strings"
)

// M is a shorthand for a settings subtree.
type M = map[string]any

// DeepCopy returns a deep copy of maps/slices; scalars are returned as-is.
func DeepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = DeepCopy(val)
		}
		return out
	case map[any]any: // tolerate yaml.v2 style maps
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[toStr(k)] = DeepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = DeepCopy(val)
		}
		return out
	case []map[string]any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = DeepCopy(val)
		}
		return out
	default:
		return v
	}
}

// CopyMap deep-copies a map (nil -> empty map).
func CopyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return DeepCopy(m).(map[string]any)
}

// MergeDefaults fills missing keys of current from defaults recursively,
// exactly like Python merge_defaults(). Non-dict values in current win.
func MergeDefaults(defaults, current any) any {
	dm, ok1 := defaults.(map[string]any)
	cm, ok2 := current.(map[string]any)
	if ok1 && ok2 {
		merged := CopyMap(cm)
		for k, dv := range dm {
			if cv, ok := merged[k]; ok {
				merged[k] = MergeDefaults(dv, cv)
			} else {
				merged[k] = DeepCopy(dv)
			}
		}
		return merged
	}
	return DeepCopy(current)
}

// Get walks a path of keys; returns nil if any step is missing.
func Get(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = mm[p]
		if !ok {
			return nil
		}
	}
	return cur
}

// Sub returns the sub-map at path, creating intermediate maps when create is true.
func Sub(m map[string]any, create bool, path ...string) map[string]any {
	cur := m
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			if !create {
				return nil
			}
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	return cur
}

// AsMap converts v to a map or returns nil.
func AsMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// AsList converts v to a []any or returns nil.
func AsList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case []map[string]any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = t[i]
		}
		return out
	}
	return nil
}

// ToFloat coerces numbers/bools/numeric strings. ok=false when not convertible.
func ToFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// Float returns v as float64, or def when missing/invalid.
func Float(v any, def float64) float64 {
	if f, ok := ToFloat(v); ok {
		return f
	}
	return def
}

// ToInt mimics Python int(): floats truncate toward zero, strings must be integral.
func ToInt(v any) (int, bool) {
	switch t := v.(type) {
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, false
		}
		return i, true
	}
	f, ok := ToFloat(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return int(f), true
}

// Int returns v as int, or def.
func Int(v any, def int) int {
	if i, ok := ToInt(v); ok {
		return i
	}
	return def
}

// Bool mimics Python truthiness for config values; nil -> def.
func Bool(v any, def bool) bool {
	switch t := v.(type) {
	case nil:
		return def
	case bool:
		return t
	case string:
		return t != ""
	}
	if f, ok := ToFloat(v); ok {
		return f != 0
	}
	return def
}

// Str returns v as string (nil -> def).
func Str(v any, def string) string {
	if v == nil {
		return def
	}
	return toStr(v)
}

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	}
	if f, ok := ToFloat(v); ok {
		if f == math.Trunc(f) && math.Abs(f) < 1e15 {
			if _, isFloat := v.(float64); isFloat {
				return strconv.FormatFloat(f, 'f', 1, 64)
			}
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return ""
}

// normalize converts all numbers to float64 so that equality comparisons do
// not trip over int/float differences introduced by YAML/JSON round-trips.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalize(val)
		}
		return out
	case bool, string, nil:
		return t
	}
	if f, ok := ToFloat(v); ok {
		return f
	}
	return v
}

// Equal compares two settings subtrees, ignoring numeric type differences.
func Equal(a, b any) bool {
	return reflect.DeepEqual(normalize(a), normalize(b))
}

// Clamp limits f to [lo, hi].
func Clamp(f, lo, hi float64) float64 {
	if f < lo {
		return lo
	}
	if f > hi {
		return hi
	}
	return f
}

// ClampInt limits i to [lo, hi].
func ClampInt(i, lo, hi int) int {
	if i < lo {
		return lo
	}
	if i > hi {
		return hi
	}
	return i
}
