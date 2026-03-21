package agent

import (
	"strings"

	"google.golang.org/genai"
)

var genaiTypes = map[string]bool{
	"OBJECT":  true,
	"STRING":  true,
	"ARRAY":   true,
	"INTEGER": true,
	"NUMBER":  true,
	"BOOLEAN": true,
}

var typeMap = map[string]genai.Type{
	"string":  genai.TypeString,
	"int":     genai.TypeInteger,
	"integer": genai.TypeInteger,
	"number":  genai.TypeNumber,
	"bool":    genai.TypeBoolean,
	"boolean": genai.TypeBoolean,
}

// ExpandOutputSchema converts a compact DSL map into a full *genai.Schema.
// If the input already looks like a full schema (has an uppercase "type" key),
// it is converted directly. Otherwise, it is treated as compact DSL.
// Returns nil for nil or empty input.
func ExpandOutputSchema(input map[string]interface{}) *genai.Schema {
	if input == nil {
		return nil
	}

	if isFullSchema(input) {
		return convertFullSchema(input)
	}

	return expandObject(input)
}

func isFullSchema(obj map[string]interface{}) bool {
	t, ok := obj["type"].(string)
	return ok && genaiTypes[t]
}

func expandObject(obj map[string]interface{}) *genai.Schema {
	properties := make(map[string]*genai.Schema)
	var required []string

	for rawKey, value := range obj {
		isArray := strings.Contains(rawKey, "[]")
		isOptional := strings.HasSuffix(rawKey, "?")
		cleanKey := strings.ReplaceAll(rawKey, "[]", "")
		cleanKey = strings.TrimSuffix(cleanKey, "?")

		if !isOptional {
			required = append(required, cleanKey)
		}

		schema := expandValue(value)
		if isArray {
			schema = &genai.Schema{Type: genai.TypeArray, Items: schema}
		}

		properties[cleanKey] = schema
	}

	result := &genai.Schema{
		Type:       genai.TypeObject,
		Properties: properties,
	}

	if len(required) > 0 {
		result.Required = required
	}

	return result
}

func expandValue(value interface{}) *genai.Schema {
	switch v := value.(type) {
	case string:
		return expandTypeString(v)
	case map[string]interface{}:
		return expandObject(v)
	default:
		return &genai.Schema{Type: genai.TypeString}
	}
}

func expandTypeString(s string) *genai.Schema {
	if t, ok := typeMap[s]; ok {
		return &genai.Schema{Type: t}
	}

	if strings.Contains(s, "|") {
		parts := strings.Split(s, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}

		return &genai.Schema{Type: genai.TypeString, Enum: parts}
	}

	return &genai.Schema{Type: genai.TypeString}
}

// convertFullSchema converts a map that already has genai.Schema structure
// into a proper *genai.Schema value.
func convertFullSchema(obj map[string]interface{}) *genai.Schema {
	schema := &genai.Schema{}

	if t, ok := obj["type"].(string); ok {
		schema.Type = genai.Type(t)
	}

	if props, ok := obj["properties"].(map[string]interface{}); ok {
		schema.Properties = make(map[string]*genai.Schema)

		for k, v := range props {
			if pm, ok := v.(map[string]interface{}); ok {
				schema.Properties[k] = convertFullSchema(pm)
			}
		}
	}

	if items, ok := obj["items"].(map[string]interface{}); ok {
		schema.Items = convertFullSchema(items)
	}

	if req, ok := obj["required"].([]interface{}); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				schema.Required = append(schema.Required, s)
			}
		}
	}

	if enum, ok := obj["enum"].([]interface{}); ok {
		for _, e := range enum {
			if s, ok := e.(string); ok {
				schema.Enum = append(schema.Enum, s)
			}
		}
	}

	if desc, ok := obj["description"].(string); ok {
		schema.Description = desc
	}

	return schema
}
