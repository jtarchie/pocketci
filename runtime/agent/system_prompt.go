package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jtarchie/pocketci/runtime/agent/schema"
)

// buildSystemInstruction constructs the system instruction describing the
// agent's environment. The user's task prompt is sent separately as a user
// message so the model receives it in the correct context-window slot.
func buildSystemInstruction(config AgentConfig, maxTurns int) string {
	var b strings.Builder

	b.WriteString("You are operating inside a CI/CD pipeline run.\n")
	b.WriteString("\n")

	if config.Image != "" {
		fmt.Fprintf(&b, "Container image: %s\n", config.Image)
	}

	if config.RunID != "" {
		fmt.Fprintf(&b, "Pipeline run ID: %s\n", config.RunID)
	}

	if config.PipelineID != "" {
		fmt.Fprintf(&b, "Pipeline ID: %s\n", config.PipelineID)
	}

	if config.TriggeredBy != "" {
		fmt.Fprintf(&b, "Triggered by: %s\n", config.TriggeredBy)
	}

	if len(config.Mounts) > 0 {
		b.WriteString("\nAvailable volumes (accessible as relative paths from the working directory):\n")

		for name := range config.Mounts {
			fmt.Fprintf(&b, "  - %s/\n", name)
		}
	}

	b.WriteString("\nTools available:\n")
	b.WriteString("  - run_script: run a multi-line /bin/sh script\n")
	b.WriteString("  - read_file: read a volume file by path without a shell (e.g. \"diff/pr.diff\")\n")
	b.WriteString("  - list_tasks: list all tasks in the current run with their statuses (pre-fetched at start)\n")
	b.WriteString("  - get_task_result: retrieve stdout, stderr, and exit code for a specific task by name\n")

	b.WriteString("\nEfficiency rules:\n")
	b.WriteString("  - Each tool call costs one full LLM round-trip. Minimise calls.\n")
	b.WriteString("  - When you need multiple sequential shell steps, combine them into ONE run_script call (use 'set -e' so failures abort early).\n")
	b.WriteString("  - Only use separate tool calls when you need to branch on intermediate output.\n")
	b.WriteString("  - If context already contains the data you need (injected task results, volume file contents), do NOT re-read it with a tool call.\n")

	fmt.Fprintf(&b, "\nYou have a budget of %d turns. Use run_script to combine steps and finish well within this limit.\n", maxTurns)

	if config.OutputSchema != nil {
		expanded := schema.ExpandOutputSchema(config.OutputSchema)
		if expanded != nil {
			schemaJSON, err := json.Marshal(expanded)
			if err == nil {
				b.WriteString("\nOutput format:\n")
				b.WriteString("Your FINAL response must be valid JSON conforming to this schema:\n")
				fmt.Fprintf(&b, "%s\n", schemaJSON)
				b.WriteString("Do not include any text outside the JSON object in your final response.\n")
			}
		}
	}

	return b.String()
}
