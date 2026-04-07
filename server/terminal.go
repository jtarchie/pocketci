package server

import (
	"encoding/json"
	"strconv"
	"strings"

	terminal "github.com/buildkite/terminal-to-html/v3"
)

type TerminalLogEntry struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

func ParseTerminalLogs(raw any) []TerminalLogEntry {
	entries, ok := raw.([]any)
	if !ok {
		if rawJSON, ok := raw.(string); ok {
			var fromJSON []TerminalLogEntry
			err := json.Unmarshal([]byte(rawJSON), &fromJSON)
			if err == nil {
				return fromJSON
			}
		}

		return nil
	}

	logs := make([]TerminalLogEntry, 0, len(entries))
	for _, entry := range entries {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}

		entryType, _ := entryMap["type"].(string)
		content, _ := entryMap["content"].(string)
		if entryType == "" || content == "" {
			continue
		}

		logs = append(logs, TerminalLogEntry{Type: entryType, Content: content})
	}

	return logs
}

// SanitizeTerminalID converts a storage path into a valid HTML ID.
func SanitizeTerminalID(fullPath string) string {
	id := strings.ReplaceAll(fullPath, "/", "_")
	id = strings.TrimLeft(id, "_")

	return id
}

// WrapTerminalLines wraps each line of terminal HTML output with numbered
// anchors for permalink linking, similar to GitHub's code view.
func WrapTerminalLines(html string, terminalID string) string {
	if html == "" {
		return ""
	}

	lineCount := strings.Count(html, "\n") + 1

	var sb strings.Builder

	sb.Grow(len(html) + lineCount*100)

	lineNum := 1
	start := 0

	for i := 0; i <= len(html); i++ {
		if i == len(html) || html[i] == '\n' {
			line := html[start:i]
			numStr := strconv.Itoa(lineNum)

			sb.WriteString(`<div class="term-line" id="`)
			sb.WriteString(terminalID)
			sb.WriteString("-L")
			sb.WriteString(numStr)
			sb.WriteString(`"><a class="term-line-num" href="#`)
			sb.WriteString(terminalID)
			sb.WriteString("-L")
			sb.WriteString(numStr)
			sb.WriteString(`">`)
			sb.WriteString(numStr)
			sb.WriteString(`</a><span class="term-line-content">`)
			sb.WriteString(line)
			sb.WriteString("</span></div>")

			lineNum++
			start = i + 1
		}
	}

	return sb.String()
}

func ToTerminalHTML(text string) string {
	return terminal.Render([]byte(text))
}

func ToTerminalHTMLFromLogs(logs []TerminalLogEntry) string {
	if len(logs) == 0 {
		return ""
	}

	var combined []byte
	for _, log := range logs {
		combined = append(combined, log.Content...)
	}

	return terminal.Render(combined)
}
