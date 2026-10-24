package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// validateToolArguments enforces the common JSON Schema fields used by domain
// tools without adding a schema dependency to the runtime. Unsupported schema
// keywords are left to the tool handler, which remains the final authority.
func validateToolArguments(schema, raw json.RawMessage) error {
	var arguments map[string]any
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return fmt.Errorf("tool arguments are not valid JSON: %w", err)
	}
	if arguments == nil {
		return errors.New("tool arguments must be a JSON object")
	}
	if len(schema) == 0 {
		return nil
	}
	var definition struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &definition); err != nil {
		return fmt.Errorf("tool has an invalid argument schema: %w", err)
	}
	if definition.Type != "" && definition.Type != "object" {
		return fmt.Errorf("tool argument schema must describe an object, got %q", definition.Type)
	}
	for _, required := range definition.Required {
		if _, ok := arguments[required]; !ok {
			return fmt.Errorf("tool argument %q is required", required)
		}
	}
	for name, rawProperty := range definition.Properties {
		value, present := arguments[name]
		if !present {
			continue
		}
		var property struct {
			Type    string   `json:"type"`
			Enum    []any    `json:"enum"`
			Minimum *float64 `json:"minimum"`
			Maximum *float64 `json:"maximum"`
		}
		if err := json.Unmarshal(rawProperty, &property); err != nil {
			return fmt.Errorf("tool argument schema for %q is invalid", name)
		}
		if !matchesJSONType(property.Type, value) {
			return fmt.Errorf("tool argument %q must be %s", name, property.Type)
		}
		if len(property.Enum) > 0 {
			matched := false
			for _, candidate := range property.Enum {
				if reflect.DeepEqual(candidate, value) {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("tool argument %q is not an allowed value", name)
			}
		}
		if numeric, ok := value.(float64); ok {
			if property.Minimum != nil && numeric < *property.Minimum {
				return fmt.Errorf("tool argument %q is below its minimum", name)
			}
			if property.Maximum != nil && numeric > *property.Maximum {
				return fmt.Errorf("tool argument %q exceeds its maximum", name)
			}
		}
	}
	return nil
}

// ValidateToolSchema checks the subset of a tool schema that the harness
// enforces locally: object type, property definitions, and required keys.
func ValidateToolSchema(schema json.RawMessage) error {
	if len(schema) == 0 {
		return errors.New("tool schema is empty")
	}
	var definition struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &definition); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if definition.Type != "object" {
		return errors.New("tool schema type must be object")
	}
	for _, required := range definition.Required {
		if _, ok := definition.Properties[required]; !ok {
			return fmt.Errorf("required property %q is not declared", required)
		}
	}
	for name, rawProperty := range definition.Properties {
		var property struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawProperty, &property); err != nil {
			return fmt.Errorf("property %q is invalid", name)
		}
		if !validSchemaType(property.Type) {
			return fmt.Errorf("property %q has unsupported type %q", name, property.Type)
		}
	}
	return nil
}

func validSchemaType(value string) bool {
	switch value {
	case "string", "integer", "number", "boolean", "object", "array":
		return true
	default:
		return false
	}
}

func matchesJSONType(wanted string, value any) bool {
	switch wanted {
	case "", "any":
		return true
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case "number":
		_, ok := value.(float64)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	default:
		return true
	}
}
