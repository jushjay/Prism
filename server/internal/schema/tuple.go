package schema

import (
	"strconv"
	"strings"
)

func hasTupleWalk(node JSONSchema, seen map[uintptr]struct{}) bool {
	if node == nil {
		return false
	}
	ptr := ptrOfMap(node)
	if _, ok := seen[ptr]; ok {
		return false
	}
	seen[ptr] = struct{}{}

	if _, ok := node["prefixItems"].([]any); ok {
		return true
	}

	if properties := asMap(node["properties"]); properties != nil {
		for _, raw := range properties {
			if child := asMap(raw); child != nil && hasTupleWalk(child, seen) {
				return true
			}
		}
	}

	if items := asMap(node["items"]); items != nil && hasTupleWalk(items, seen) {
		return true
	}

	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if entries, ok := node[key].([]any); ok {
			for _, raw := range entries {
				if child := asMap(raw); child != nil && hasTupleWalk(child, seen) {
					return true
				}
			}
		}
	}

	for _, key := range []string{"$defs", "definitions"} {
		if defs := asMap(node[key]); defs != nil {
			for _, raw := range defs {
				if child := asMap(raw); child != nil && hasTupleWalk(child, seen) {
					return true
				}
			}
		}
	}

	for _, key := range []string{"if", "then", "else", "not"} {
		if child := asMap(node[key]); child != nil && hasTupleWalk(child, seen) {
			return true
		}
	}

	return false
}

func convertTupleSchemas(node JSONSchema) JSONSchema {
	return convertTupleWalk(node, map[uintptr]struct{}{})
}

func convertTupleWalk(node JSONSchema, seen map[uintptr]struct{}) JSONSchema {
	if node == nil {
		return nil
	}
	ptr := ptrOfMap(node)
	if _, ok := seen[ptr]; ok {
		return node
	}
	seen[ptr] = struct{}{}

	if prefixItems, ok := node["prefixItems"].([]any); ok {
		properties := map[string]any{}
		required := make([]any, 0, len(prefixItems))
		for index, raw := range prefixItems {
			key := strconv.Itoa(index)
			if child := asMap(raw); child != nil {
				properties[key] = convertTupleWalk(child, seen)
			} else {
				properties[key] = raw
			}
			required = append(required, key)
		}
		node["type"] = "object"
		node["properties"] = properties
		node["required"] = required
		node["additionalProperties"] = false
		delete(node, "prefixItems")
		delete(node, "items")
		return node
	}

	if properties := asMap(node["properties"]); properties != nil {
		for key, raw := range properties {
			if child := asMap(raw); child != nil {
				properties[key] = convertTupleWalk(child, seen)
			}
		}
	}
	if items := asMap(node["items"]); items != nil {
		node["items"] = convertTupleWalk(items, seen)
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if entries, ok := node[key].([]any); ok {
			for index, raw := range entries {
				if child := asMap(raw); child != nil {
					entries[index] = convertTupleWalk(child, seen)
				}
			}
		}
	}
	for _, key := range []string{"$defs", "definitions"} {
		if defs := asMap(node[key]); defs != nil {
			for name, raw := range defs {
				if child := asMap(raw); child != nil {
					defs[name] = convertTupleWalk(child, seen)
				}
			}
		}
	}
	for _, key := range []string{"if", "then", "else", "not"} {
		if child := asMap(node[key]); child != nil {
			node[key] = convertTupleWalk(child, seen)
		}
	}
	return node
}

func reconvertTupleValues(data any, schema JSONSchema, root JSONSchema) any {
	if schema == nil {
		return data
	}
	if ref, _ := schema["$ref"].(string); ref != "" {
		if resolved := resolveRef(ref, root); resolved != nil {
			return reconvertTupleValues(data, resolved, root)
		}
		return data
	}

	if prefixItems, ok := schema["prefixItems"].([]any); ok {
		if object, ok := data.(map[string]any); ok {
			result := make([]any, 0, len(prefixItems))
			for index, raw := range prefixItems {
				key := strconv.Itoa(index)
				value := object[key]
				if child := asMap(raw); child != nil {
					result = append(result, reconvertTupleValues(value, child, root))
				} else {
					result = append(result, value)
				}
			}
			return result
		}
	}

	if properties := asMap(schema["properties"]); properties != nil {
		if object, ok := data.(map[string]any); ok {
			cloned := cloneMap(object)
			for key, raw := range properties {
				if child := asMap(raw); child != nil {
					cloned[key] = reconvertTupleValues(cloned[key], child, root)
				}
			}
			return cloned
		}
	}

	if items := asMap(schema["items"]); items != nil {
		if list, ok := data.([]any); ok {
			output := make([]any, len(list))
			for index, value := range list {
				output[index] = reconvertTupleValues(value, items, root)
			}
			return output
		}
	}

	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if entries, ok := schema[key].([]any); ok {
			for _, raw := range entries {
				if child := asMap(raw); child != nil && HasTupleSchemas(child) {
					return reconvertTupleValues(data, child, root)
				}
			}
		}
	}

	return data
}

func resolveRef(ref string, root JSONSchema) JSONSchema {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	if len(parts) != 2 {
		return nil
	}
	defs := asMap(root[parts[0]])
	if defs == nil {
		return nil
	}
	return asMap(defs[parts[1]])
}
