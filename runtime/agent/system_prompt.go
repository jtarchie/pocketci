package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jtarchie/pocketci/runtime/agent/schema"
)

// BuildSystemInstruction constructs the system instruction describing the
// agent's environment. The user's task prompt is sent separately as a user
// message so the model receives it in the correct context-window slot.
func BuildSystemInstruction(config AgentConfig, maxTurns int) string {
	var b strings.Builder

	b.WriteString("You are an AI agent operating inside a CI/CD pipeline run. Use the tools available to you to complete the task described in the user message.\n")

	// <environment> — runtime metadata
	var env strings.Builder

	if config.Image != "" {
		fmt.Fprintf(&env, "Container image: %s\n", config.Image)
	}

	if config.RunID != "" {
		fmt.Fprintf(&env, "Pipeline run ID: %s\n", config.RunID)
	}

	if config.PipelineID != "" {
		fmt.Fprintf(&env, "Pipeline ID: %s\n", config.PipelineID)
	}

	if config.TriggeredBy != "" {
		fmt.Fprintf(&env, "Triggered by: %s\n", config.TriggeredBy)
	}

	if env.Len() > 0 {
		b.WriteString("\n<environment>\n")
		b.WriteString(env.String())
		b.WriteString("</environment>\n")
	}

	// <volumes> — mounted paths
	if len(config.Mounts) > 0 {
		b.WriteString("\n<volumes>\n")

		for name := range config.Mounts {
			fmt.Fprintf(&b, "- %s/\n", name)
		}

		b.WriteString("</volumes>\n")
	}

	// <tools> — selection guidance (full descriptions come from ADK metadata)
	b.WriteString("\n<tools>\n")
	b.WriteString("Use dedicated tools instead of run_script when possible:\n")
	b.WriteString("- To read files use read_file instead of cat, head, or sed.\n")
	b.WriteString("- To search file contents use grep instead of shell grep or rg.\n")
	b.WriteString("- To find files by name use glob instead of shell find or ls.\n")
	b.WriteString("- To write files use write_file instead of shell redirection.\n")
	b.WriteString("- Use run_script only for commands that have no dedicated tool (e.g. git).\n")
	b.WriteString("</tools>\n")

	// <efficiency> — budget and call minimisation
	b.WriteString("\n<efficiency>\n")
	b.WriteString("- Each tool call costs one LLM round-trip. Minimise calls.\n")
	b.WriteString("- Combine sequential shell steps into one run_script call with 'set -e'.\n")
	b.WriteString("- If context already contains the data you need, do not re-read it.\n")
	fmt.Fprintf(&b, "- Budget: %d turns.\n", maxTurns)
	b.WriteString("</efficiency>\n")

	// <output_format> — only when output_schema is configured
	if config.OutputSchema != nil {
		expanded := schema.ExpandOutputSchema(config.OutputSchema)
		if expanded != nil {
			schemaJSON, err := json.Marshal(expanded)
			if err == nil {
				b.WriteString("\n<output_format>\n")
				b.WriteString("Your FINAL response must be valid JSON conforming to this schema:\n")
				fmt.Fprintf(&b, "%s\n", schemaJSON)
				b.WriteString("Do not include any text outside the JSON object.\n")
				b.WriteString("</output_format>\n")
			}
		}
	}

	return b.String()
}
