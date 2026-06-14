package schema

import "reflect"

func isMap(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

func asMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneValue(value)
	}
	return output
}

func cloneSlice(input []any) []any {
	output := make([]any, len(input))
	for index, value := range input {
		output[index] = cloneValue(value)
	}
	return output
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		return cloneSlice(typed)
	default:
		return typed
	}
}

func ptrOfMap(input map[string]any) uintptr {
	return reflect.ValueOf(input).Pointer()
}
