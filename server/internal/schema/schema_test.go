package schema

import (
	"reflect"
	"testing"
)

func TestPrepareInjectsAdditionalProperties(t *testing.T) {
	input := JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"user": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
		},
	}

	prepared, original := Prepare(input)
	if original != nil {
		t.Fatal("did not expect original tuple schema")
	}
	user := prepared["properties"].(map[string]any)["user"].(map[string]any)
	if user["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false, got %#v", user["additionalProperties"])
	}
}

func TestPrepareConvertsTupleSchemaAndReconvert(t *testing.T) {
	input := JSONSchema{
		"type": "array",
		"prefixItems": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
		},
	}

	prepared, original := Prepare(input)
	if original == nil {
		t.Fatal("expected original tuple schema")
	}
	if prepared["type"] != "object" {
		t.Fatalf("expected converted object type, got %#v", prepared["type"])
	}

	converted := map[string]any{"0": "hello", "1": 42.0}
	reconverted := ReconvertTupleValues(converted, original)
	expected := []any{"hello", 42.0}
	if !reflect.DeepEqual(reconverted, expected) {
		t.Fatalf("expected %#v, got %#v", expected, reconverted)
	}
}
