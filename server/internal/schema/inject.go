package schema

func injectAdditionalProperties(schema JSONSchema) JSONSchema {
	return injectWalk(schema, map[uintptr]struct{}{})
}

func injectWalk(node JSONSchema, seen map[uintptr]struct{}) JSONSchema {
	if node == nil {
		return nil
	}
	ptr := ptrOfMap(node)
	if _, ok := seen[ptr]; ok {
		return node
	}
	seen[ptr] = struct{}{}

	if nodeType, _ := node["type"].(string); nodeType == "object" {
		if _, exists := node["additionalProperties"]; !exists {
			node["additionalProperties"] = false
		}
	}

	if properties := asMap(node["properties"]); properties != nil {
		for key, raw := range properties {
			if child := asMap(raw); child != nil {
				properties[key] = injectWalk(child, seen)
			}
		}
	}

	if patternProps := asMap(node["patternProperties"]); patternProps != nil {
		for key, raw := range patternProps {
			if child := asMap(raw); child != nil {
				patternProps[key] = injectWalk(child, seen)
			}
		}
	}

	for _, defsKey := range []string{"$defs", "definitions"} {
		if defs := asMap(node[defsKey]); defs != nil {
			for key, raw := range defs {
				if child := asMap(raw); child != nil {
					defs[key] = injectWalk(child, seen)
				}
			}
		}
	}

	if items := asMap(node["items"]); items != nil {
		node["items"] = injectWalk(items, seen)
	}

	if prefixItems, ok := node["prefixItems"].([]any); ok {
		for index, raw := range prefixItems {
			if child := asMap(raw); child != nil {
				prefixItems[index] = injectWalk(child, seen)
			}
		}
	}

	for _, combiner := range []string{"oneOf", "anyOf", "allOf"} {
		if entries, ok := node[combiner].([]any); ok {
			for index, raw := range entries {
				if child := asMap(raw); child != nil {
					entries[index] = injectWalk(child, seen)
				}
			}
		}
	}

	for _, conditional := range []string{"if", "then", "else", "not"} {
		if child := asMap(node[conditional]); child != nil {
			node[conditional] = injectWalk(child, seen)
		}
	}

	return node
}
