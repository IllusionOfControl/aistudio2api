package aistudio

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestEncodeJSONSchema_NullFields verifies encoding of optional null fields and data null values
func TestEncodeJSONSchema_NullFields(t *testing.T) {
	fields := []string{"format", "description", "nullable", "enum", "items", "properties", "required", "minItems", "maxItems", "minProperties", "maxProperties", "minimum", "maximum", "minLength", "maxLength", "pattern", "oneOf", "anyOf", "allOf", "not", "default"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			wire, err := encodeJSONSchema(json.RawMessage(`{"type":"string","` + field + `":null}`))
			if err != nil || !reflect.DeepEqual(wire, []any{int64(1)}) {
				t.Fatalf("wire=%#v error=%v", wire, err)
			}
		})
	}
	wire, err := encodeJSONSchema(json.RawMessage(`{"type":"string","example":null}`))
	if err != nil || len(wire) <= 15 || !reflect.DeepEqual(wire[15], []any{int64(0)}) {
		t.Fatalf("null example wire=%#v error=%v", wire, err)
	}
}

// TestEncodeJSONSchema_NotVariants verifies semantics of forbidden constraints and exclusion values
func TestEncodeJSONSchema_NotVariants(t *testing.T) {
	for _, raw := range []string{`{"type":"string","not":null}`, `{"type":"string","not":false}`} {
		wire, err := encodeJSONSchema(json.RawMessage(raw))
		if err != nil || !reflect.DeepEqual(wire, []any{int64(1)}) {
			t.Fatalf("schema=%s wire=%#v error=%v", raw, wire, err)
		}
	}
	for _, raw := range []string{`{"type":"string","not":true}`, `{"type":"string","not":{}}`, `{"type":"object","properties":{"deny":false},"required":["deny"]}`} {
		if wire, err := encodeJSONSchema(json.RawMessage(raw)); err == nil {
			t.Fatalf("forbidden schema=%s wire=%#v", raw, wire)
		}
	}
	wire, err := encodeJSONSchema(json.RawMessage(`{"type":["string","null"],"not":{"type":"null"}}`))
	if err != nil || len(wire) <= 3 || wire[3] != false {
		t.Fatalf("not null wire=%#v error=%v", wire, err)
	}
	for _, value := range []string{`""`, `"denied"`, `["a","b"]`} {
		wire, err := encodeJSONSchema(json.RawMessage(`{"type":"string","not":` + value + `}`))
		if err != nil || len(wire) <= 19 {
			t.Fatalf("not enum wire=%#v error=%v", wire, err)
		}
		excluded := wire[19].([]any)
		var expected []string
		if value[0] == '[' {
			_ = json.Unmarshal([]byte(value), &expected)
		} else {
			var text string
			_ = json.Unmarshal([]byte(value), &text)
			expected = []string{text}
		}
		if len(excluded) <= 4 || !reflect.DeepEqual(excluded[4], expected) {
			t.Fatalf("excluded=%#v expected=%#v", excluded, expected)
		}
	}
}

// TestEncodeJSONSchema_TopLevelNullOrEmpty verifies omitted parameters use object structure
func TestEncodeJSONSchema_TopLevelNullOrEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "null"} {
		wire, err := encodeJSONSchema(json.RawMessage(raw))
		if err != nil || !reflect.DeepEqual(wire, []any{int64(6)}) {
			t.Fatalf("schema=%q wire=%#v error=%v", raw, wire, err)
		}
	}
}

// TestEncodeJSONSchema_DirectConst verifies type and enum values of constant properties
func TestEncodeJSONSchema_DirectConst(t *testing.T) {
	wire, err := encodeJSONSchema(json.RawMessage(`{"type":"object","properties":{"type":{"const":"tool_call","title":"Type"}}}`))
	if err != nil || len(wire) <= 6 {
		t.Fatalf("wire=%#v error=%v", wire, err)
	}
	properties := wire[6].([]any)
	if len(properties) != 1 {
		t.Fatalf("properties=%#v", properties)
	}
	property := properties[0].([]any)
	value := property[1].([]any)
	if property[0] != "type" || len(value) <= 4 || value[0] != int64(1) || !reflect.DeepEqual(value[4], []string{"tool_call"}) {
		t.Fatalf("property=%#v", property)
	}
}
