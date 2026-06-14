package schema

type JSONSchema = map[string]any

func Prepare(input JSONSchema) (prepared JSONSchema, originalTupleSchema JSONSchema) {
	cloned := cloneMap(input)
	if !HasTupleSchemas(cloned) {
		return injectAdditionalProperties(cloned), nil
	}
	original := cloneMap(input)
	convertTupleSchemas(cloned)
	return injectAdditionalProperties(cloned), original
}

func HasTupleSchemas(schema JSONSchema) bool {
	return hasTupleWalk(schema, map[uintptr]struct{}{})
}

func ReconvertTupleValues(data any, schema JSONSchema) any {
	return reconvertTupleValues(data, schema, schema)
}
