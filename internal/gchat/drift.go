package gchat

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Schema drift is Google adding a response field without notice. AIP-180
// permits it, and a new field lands on every row of its resource at once.
//
// Rejecting unknown fields turns that routine change into a total
// outage, and it has happened twice: writes landed and then reported an
// internal error, so a caller could not tell a failure from a
// duplicate. Requiring them is just as bad in the other
// direction, because nothing would ever be noticed.
//
// So the rule is that drift must be visible and must not fail the call.
// Every response is decoded twice: once into the struct, once into a
// map. Keys the struct does not know are reported by path. Fields the
// struct declares without omitempty stay required, so a field Google
// removes or renames still fails loudly.

// DriftFunc is called once per unknown field, with a path like
// "Message.newField" or "Space.spaceDetails.newField".
type DriftFunc func(path string)

// decode unmarshals data into out and reports every field of the
// response that out does not model. A drift report never fails a call.
//
// An empty body is a success with nothing in it, not a parse failure.
// Google normally transcodes an empty protobuf to "{}", but a write that
// answers 200 with no body at all must not come back as an error: the
// write landed, and a caller told otherwise retries it. That is the
// duplicate post the client-assigned message id exists to prevent.
func decode(data []byte, out any, drift DriftFunc) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return err
	}
	if drift == nil {
		return nil
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		// The typed decode already succeeded, so this cannot normally
		// happen. Drift reporting is best effort either way.
		return nil //nolint:nilerr // the caller has its value; drift is advisory
	}
	t := reflect.TypeOf(out)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	reportUnknown(typeName(t), t, raw, drift)
	return nil
}

// typeName is the leaf name of a type, used as the first path segment.
func typeName(t reflect.Type) string {
	if t == nil {
		return "response"
	}
	if n := t.Name(); n != "" {
		return n
	}
	return t.String()
}

// reportUnknown walks value against the shape t describes.
func reportUnknown(path string, t reflect.Type, value any, drift DriftFunc) {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return
	}

	switch t.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return
		}
		fields := jsonFields(t)
		// Sorted so a response with several new fields reports them in
		// a stable order, which keeps logs and tests predictable.
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ft, known := fields[k]
			if !known {
				drift(path + "." + k)
				continue
			}
			reportUnknown(path+"."+k, ft, obj[k], drift)
		}
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return
		}
		for _, item := range items {
			reportUnknown(path, t.Elem(), item, drift)
		}
	case reflect.Map:
		obj, ok := value.(map[string]any)
		if !ok {
			return
		}
		// A map models an open set of keys, so no key here is unknown.
		// Its values still have a declared type worth walking.
		for _, v := range obj {
			reportUnknown(path, t.Elem(), v, drift)
		}
	default:
		// A scalar or an any-typed field models nothing to check.
	}
}

// fieldCache memoises jsonFields. A search scanning fifty pages walks
// five thousand messages, and the field map of a type never changes.
var fieldCache sync.Map

// jsonFields maps a struct's wire names to their types, following
// embedded structs the way encoding/json does.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.(map[string]reflect.Type)
	}
	out := buildJSONFields(t)
	fieldCache.Store(t, out)
	return out
}

// buildJSONFields does the reflection. The result is read-only once it
// reaches the cache.
func buildJSONFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				for k, v := range jsonFields(et) {
					out[k] = v
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}
