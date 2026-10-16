package localdag

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func object(properties map[string]any) map[string]any {
	required := []string{}
	for key := range properties {
		required = append(required, key)
	}
	// Stable schema serialization is part of the saved request identity.
	sort.Strings(required)
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func array(items any) map[string]any { return map[string]any{"type": "array", "items": items} }
func stringType() map[string]any     { return map[string]any{"type": "string"} }
func schema(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

var stageSchema = schema(object(map[string]any{
	"action": map[string]any{"type": "string", "enum": []string{"continue", "start", "none", "needs_context"}},
	"stage":  map[string]any{"anyOf": []any{object(map[string]any{"objective": stringType(), "summary": stringType(), "approach": stringType()}), map[string]any{"type": "null"}}},
	"items":  object(map[string]any{"add": array(stringType()), "revise": array(object(map[string]any{"id": stringType(), "text": stringType()})), "complete": array(stringType()), "withdraw": array(stringType())}),
	"reason": stringType(), "evidence": array(stringType()),
}))
var relationSchema = schema(object(map[string]any{
	"status":  map[string]any{"type": "string", "enum": []string{"complete", "needs_context"}},
	"parents": array(object(map[string]any{"id": stringType(), "reason": stringType(), "evidence": array(stringType())})),
	"reason":  stringType(),
}))

func decodeOutput(raw string, shape json.RawMessage, target any) error {
	var schemaValue any
	if err := json.Unmarshal(shape, &schemaValue); err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", schemaValue); err != nil {
		return err
	}
	compiled, err := compiler.Compile("schema.json")
	if err != nil {
		return err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return errors.New("model output is not JSON")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("model output must be one JSON value")
	}
	if err := compiled.Validate(value); err != nil {
		return errors.New("model output does not match the response schema")
	}
	return json.Unmarshal([]byte(raw), target)
}
