package schema

import (
	"encoding/json"
	"fmt"
	"math"
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

// ValidateJSON checks whether a JSON string conforms to the given genai.Schema.
// Returns nil if valid, or an error describing the first validation failure.
func ValidateJSON(jsonStr string, s *genai.Schema) error {
	var parsed interface{}
	err := json.Unmarshal([]byte(jsonStr), &parsed)
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	return validateValue(parsed, s, "")
}

func validateValue(value interface{}, s *genai.Schema, path string) error {
	if s == nil {
		return nil
	}

	switch s.Type {
	case genai.TypeObject:
		return validateObject(value, s, path)
	case genai.TypeArray:
		return validateArray(value, s, path)
	case genai.TypeString:
		return validateString(value, s, path)
	case genai.TypeInteger:
		return validateInteger(value, path)
	case genai.TypeNumber:
		return validateNumber(value, path)
	case genai.TypeBoolean:
		return validateBoolean(value, path)
	case genai.TypeUnspecified, genai.TypeNULL:
		return nil
	}

	return nil
}

func fieldPath(parent, key string) string {
	if parent == "" {
		return key
	}

	return parent + "." + key
}

func validateObject(value interface{}, s *genai.Schema, path string) error {
	obj, ok := value.(map[string]interface{})
	if !ok {
		return fmt.Errorf("expected object at %q, got %T", path, value)
	}

	for _, req := range s.Required {
		if _, exists := obj[req]; !exists {
			return fmt.Errorf("missing required field %q", fieldPath(path, req))
		}
	}

	for key, propSchema := range s.Properties {
		val, exists := obj[key]
		if !exists {
			continue
		}

		err := validateValue(val, propSchema, fieldPath(path, key))
		if err != nil {
			return err
		}
	}

	return nil
}

func validateArray(value interface{}, s *genai.Schema, path string) error {
	arr, ok := value.([]interface{})
	if !ok {
		return fmt.Errorf("expected array at %q, got %T", path, value)
	}

	if s.Items == nil {
		return nil
	}

	for i, elem := range arr {
		elemPath := fmt.Sprintf("%s[%d]", path, i)
		err := validateValue(elem, s.Items, elemPath)
		if err != nil {
			return err
		}
	}

	return nil
}

func validateString(value interface{}, s *genai.Schema, path string) error {
	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("expected string at %q, got %T", path, value)
	}

	if len(s.Enum) > 0 {
		for _, e := range s.Enum {
			if str == e {
				return nil
			}
		}

		return fmt.Errorf("value %q at %q not in enum %v", str, path, s.Enum)
	}

	return nil
}

func validateInteger(value interface{}, path string) error {
	num, ok := value.(float64)
	if !ok {
		return fmt.Errorf("expected integer at %q, got %T", path, value)
	}

	if num != math.Trunc(num) {
		return fmt.Errorf("expected integer at %q, got float %v", path, num)
	}

	return nil
}

func validateNumber(value interface{}, path string) error {
	if _, ok := value.(float64); !ok {
		return fmt.Errorf("expected number at %q, got %T", path, value)
	}

	return nil
}

func validateBoolean(value interface{}, path string) error {
	if _, ok := value.(bool); !ok {
		return fmt.Errorf("expected boolean at %q, got %T", path, value)
	}

	return nil
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
