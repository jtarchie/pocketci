package schema_test

import (
	"sort"
	"testing"

	. "github.com/onsi/gomega"
	"google.golang.org/genai"

	"github.com/jtarchie/pocketci/runtime/agent/schema"
)

func TestExpandOutputSchema(t *testing.T) {
	t.Parallel()

	t.Run("nil input returns nil", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		assert.Expect(schema.ExpandOutputSchema(nil)).To(BeNil())
	})

	t.Run("passthrough full genai.Schema OBJECT unchanged", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		input := map[string]interface{}{
			"type": "OBJECT",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "STRING"},
			},
			"required": []interface{}{"name"},
		}

		result := schema.ExpandOutputSchema(input)
		assert.Expect(result.Type).To(Equal(genai.TypeObject))
		assert.Expect(result.Properties).To(HaveKey("name"))
		assert.Expect(result.Properties["name"].Type).To(Equal(genai.TypeString))
		assert.Expect(result.Required).To(ConsistOf("name"))
	})

	t.Run("passthrough full schema with ARRAY type", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		input := map[string]interface{}{
			"type":  "ARRAY",
			"items": map[string]interface{}{"type": "STRING"},
		}

		result := schema.ExpandOutputSchema(input)
		assert.Expect(result.Type).To(Equal(genai.TypeArray))
		assert.Expect(result.Items).NotTo(BeNil())
		assert.Expect(result.Items.Type).To(Equal(genai.TypeString))
	})

	t.Run("simple string field", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"name": "string",
		})

		assert.Expect(result.Type).To(Equal(genai.TypeObject))
		assert.Expect(result.Properties).To(HaveKey("name"))
		assert.Expect(result.Properties["name"].Type).To(Equal(genai.TypeString))
		assert.Expect(result.Required).To(ConsistOf("name"))
	})

	t.Run("optional field excluded from required", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"name?": "string",
		})

		assert.Expect(result.Properties).To(HaveKey("name"))
		assert.Expect(result.Properties["name"].Type).To(Equal(genai.TypeString))
		assert.Expect(result.Required).To(BeEmpty())
	})

	t.Run("integer type via int alias", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"count": "int",
		})

		assert.Expect(result.Properties["count"].Type).To(Equal(genai.TypeInteger))
		assert.Expect(result.Required).To(ConsistOf("count"))
	})

	t.Run("integer type via integer alias", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"count": "integer",
		})

		assert.Expect(result.Properties["count"].Type).To(Equal(genai.TypeInteger))
	})

	t.Run("number type", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"score": "number",
		})

		assert.Expect(result.Properties["score"].Type).To(Equal(genai.TypeNumber))
		assert.Expect(result.Required).To(ConsistOf("score"))
	})

	t.Run("boolean type via bool alias", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"active": "bool",
		})

		assert.Expect(result.Properties["active"].Type).To(Equal(genai.TypeBoolean))
	})

	t.Run("boolean type via boolean alias", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"active": "boolean",
		})

		assert.Expect(result.Properties["active"].Type).To(Equal(genai.TypeBoolean))
	})

	t.Run("enum via pipe-separated values", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"severity": "critical|high|medium|low",
		})

		assert.Expect(result.Properties["severity"].Type).To(Equal(genai.TypeString))
		assert.Expect(result.Properties["severity"].Enum).To(Equal([]string{"critical", "high", "medium", "low"}))
		assert.Expect(result.Required).To(ConsistOf("severity"))
	})

	t.Run("array of strings", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"tags[]": "string",
		})

		assert.Expect(result.Properties["tags"].Type).To(Equal(genai.TypeArray))
		assert.Expect(result.Properties["tags"].Items.Type).To(Equal(genai.TypeString))
		assert.Expect(result.Required).To(ConsistOf("tags"))
	})

	t.Run("array of objects", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"issues[]": map[string]interface{}{
				"desc": "string",
			},
		})

		assert.Expect(result.Properties["issues"].Type).To(Equal(genai.TypeArray))
		items := result.Properties["issues"].Items
		assert.Expect(items.Type).To(Equal(genai.TypeObject))
		assert.Expect(items.Properties["desc"].Type).To(Equal(genai.TypeString))
		assert.Expect(items.Required).To(ConsistOf("desc"))
	})

	t.Run("optional array", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"tags[]?": "string",
		})

		assert.Expect(result.Properties["tags"].Type).To(Equal(genai.TypeArray))
		assert.Expect(result.Properties["tags"].Items.Type).To(Equal(genai.TypeString))
		assert.Expect(result.Required).To(BeEmpty())
	})

	t.Run("nested object", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"meta": map[string]interface{}{
				"author": "string",
				"year?":  "int",
			},
		})

		meta := result.Properties["meta"]
		assert.Expect(meta.Type).To(Equal(genai.TypeObject))
		assert.Expect(meta.Properties["author"].Type).To(Equal(genai.TypeString))
		assert.Expect(meta.Properties["year"].Type).To(Equal(genai.TypeInteger))
		assert.Expect(meta.Required).To(ConsistOf("author"))
		assert.Expect(result.Required).To(ConsistOf("meta"))
	})

	t.Run("mixed required and optional fields", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"name": "string",
			"bio?": "string",
			"age":  "int",
		})

		assert.Expect(result.Properties["name"].Type).To(Equal(genai.TypeString))
		assert.Expect(result.Properties["bio"].Type).To(Equal(genai.TypeString))
		assert.Expect(result.Properties["age"].Type).To(Equal(genai.TypeInteger))

		// Sort required for deterministic comparison (map iteration order).
		sort.Strings(result.Required)
		assert.Expect(result.Required).To(Equal([]string{"age", "name"}))
	})

	t.Run("all optional fields omit required", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"a?": "string",
			"b?": "int",
		})

		assert.Expect(result.Required).To(BeEmpty())
	})

	t.Run("empty object", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{})

		assert.Expect(result.Type).To(Equal(genai.TypeObject))
		assert.Expect(result.Properties).To(BeEmpty())
		assert.Expect(result.Required).To(BeEmpty())
	})

	t.Run("full review schema compact to expanded", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		compact := map[string]interface{}{
			"summary": "string",
			"issues[]": map[string]interface{}{
				"severity":    "critical|high|medium|low",
				"description": "string",
				"file?":       "string",
				"line?":       "int",
				"start_line?": "int",
			},
		}

		result := schema.ExpandOutputSchema(compact)

		assert.Expect(result.Type).To(Equal(genai.TypeObject))
		assert.Expect(result.Properties["summary"].Type).To(Equal(genai.TypeString))

		issues := result.Properties["issues"]
		assert.Expect(issues.Type).To(Equal(genai.TypeArray))

		items := issues.Items
		assert.Expect(items.Type).To(Equal(genai.TypeObject))
		assert.Expect(items.Properties["severity"].Type).To(Equal(genai.TypeString))
		assert.Expect(items.Properties["severity"].Enum).To(Equal([]string{"critical", "high", "medium", "low"}))
		assert.Expect(items.Properties["description"].Type).To(Equal(genai.TypeString))
		assert.Expect(items.Properties["file"].Type).To(Equal(genai.TypeString))
		assert.Expect(items.Properties["line"].Type).To(Equal(genai.TypeInteger))
		assert.Expect(items.Properties["start_line"].Type).To(Equal(genai.TypeInteger))

		// Sort for deterministic check.
		sort.Strings(items.Required)
		assert.Expect(items.Required).To(Equal([]string{"description", "severity"}))

		sort.Strings(result.Required)
		assert.Expect(result.Required).To(Equal([]string{"issues", "summary"}))
	})

	t.Run("full schema with description preserved", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		input := map[string]interface{}{
			"type":        "OBJECT",
			"description": "Code review results",
			"properties": map[string]interface{}{
				"summary": map[string]interface{}{"type": "STRING"},
			},
		}

		result := schema.ExpandOutputSchema(input)
		assert.Expect(result.Description).To(Equal("Code review results"))
	})

	t.Run("full schema with enum preserved", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		input := map[string]interface{}{
			"type": "STRING",
			"enum": []interface{}{"a", "b", "c"},
		}

		result := schema.ExpandOutputSchema(input)
		assert.Expect(result.Type).To(Equal(genai.TypeString))
		assert.Expect(result.Enum).To(Equal([]string{"a", "b", "c"}))
	})

	t.Run("unknown string type defaults to STRING", func(t *testing.T) {
		t.Parallel()
		assert := NewGomegaWithT(t)

		result := schema.ExpandOutputSchema(map[string]interface{}{
			"data": "unknowntype",
		})

		assert.Expect(result.Properties["data"].Type).To(Equal(genai.TypeString))
	})
}
