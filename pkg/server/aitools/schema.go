package aitools

import "encoding/json"

// The JSON Schema helpers.
//
// Schemas are built rather than written as literals so that every tool
// advertises the same shape: an object, with named properties, with
// additionalProperties refused. The last part matters — a model that invents an
// extra field should be told, not quietly ignored, because a silently dropped
// `namespace` turns a narrow read into a Cluster-wide one.

func objectSchema(properties map[string]any, required []string) json.RawMessage {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	return encoded
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func booleanProperty(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}
