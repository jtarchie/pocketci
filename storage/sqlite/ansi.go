package storage

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ansiEscape matches all ANSI/VT escape sequences and non-printable control
// characters so they can be stripped before full-text indexing.
//
// Patterns covered:
//   - CSI sequences:   ESC [ <params> <final-byte>   (colors, cursor movement, …)
//   - OSC sequences:   ESC ] … BEL  or  ESC ] … ESC \
//   - DCS/APC/PM/SOS: ESC P|_|^|X … ESC \
//   - Simple 2-char:   ESC <any single char>
//   - Lone ESC byte
//   - 8-bit C1 control codes (0x80–0x9F)
var ansiEscape = regexp.MustCompile(
	// CSI  ESC [ params final-byte
	`\x1b\[[0-?]*[ -/]*[@-~]` +
		// OSC  ESC ] ... BEL or ST
		`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` +
		// DCS / APC / PM / SOS  ESC P|_|^|X ... ST
		`|\x1b[P_\^X][^\x1b]*(?:\x1b\\)` +
		// Simple 2-char escape sequences  ESC <char>  (not already matched above)
		`|\x1b[^[\]PX_\^]` +
		// Lone ESC byte (unmatched)
		`|\x1b` +
		// 8-bit C1 control codes
		`|[\x80-\x9f]`,
)

// StripANSI removes all ANSI escape sequences from s, returning plain text
// suitable for full-text indexing.
func StripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// extractTextFromJSON recursively walks a JSON-marshaled value and collects
// all string leaf values, joining them with a single space. This produces a
// flat text representation of any payload for FTS indexing.
func extractTextFromJSON(data []byte) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return ""
	}

	var parts []string
	collectStrings(v, &parts)

	return strings.Join(parts, " ")
}

func collectStrings(v any, parts *[]string) {
	switch x := v.(type) {
	case string:
		*parts = append(*parts, x)
	case map[string]any:
		for _, val := range x {
			collectStrings(val, parts)
		}
	case []any:
		for _, val := range x {
			collectStrings(val, parts)
		}
	}
}
