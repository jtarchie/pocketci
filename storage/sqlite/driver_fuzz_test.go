package storage

import (
	"strings"
	"testing"
	"unicode"
)

// FuzzSanitizeFTSQuery codifies PCI-SEC-STORAGE-012's invariant: no input,
// no matter how adversarial, can produce a sanitized output that exposes an
// unquoted FTS5 boolean operator (AND, OR, NOT, NEAR, +, -, ^) at a top
// level. The output must always be a sequence of `"…"*` literal-prefix
// terms separated by single spaces; the only "AND"/"OR"/"NEAR" tokens
// allowed are those that appear INSIDE a `"…"` phrase (where FTS5 treats
// them as literal words).
func FuzzSanitizeFTSQuery(f *testing.F) {
	seed := []string{
		"",
		" ",
		"\t\n",
		"hello",
		"hello world",
		`"quoted"`,
		`""`,
		`AND`,
		`OR`,
		`NOT`,
		`NEAR`,
		`AND OR NOT`,
		`foo AND bar`,
		`( foo OR bar )`,
		"\x00",
		"αβγ",
		"\"^*-+:",
		strings.Repeat("a", 1024),
		"  multiple   spaces  ",
		`hello "embedded" world`,
		"col:value",
	}
	for _, s := range seed {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		out := sanitizeFTSQuery(in)

		// Empty input or whitespace-only input must produce empty output.
		if strings.TrimSpace(in) == "" {
			if out != "" {
				t.Fatalf("expected empty output for whitespace input %q, got %q", in, out)
			}
			return
		}

		// Output must be parseable as a series of `"escaped"*` terms separated
		// by single spaces. Walking the structure with a state machine
		// proves no unquoted FTS5 metacharacter can have leaked through.
		assertStructure(t, in, out)
	})
}

// assertStructure walks the sanitizer output as: ('"' chars '"' '*' (' ' | END))+
// where 'chars' is any sequence of runes with "" treated as an escaped quote.
// If the entire output matches the grammar, no top-level operator is reachable.
func assertStructure(t *testing.T, in, out string) {
	t.Helper()

	i := 0
	for i < len(out) {
		if out[i] != '"' {
			t.Fatalf("expected '\"' at offset %d of %q (input %q)", i, out, in)
		}
		i++
		// Walk to the closing quote, allowing "" as an escaped quote.
		for i < len(out) {
			if out[i] == '"' {
				if i+1 < len(out) && out[i+1] == '"' {
					i += 2
					continue
				}
				break
			}
			i++
		}
		if i >= len(out) || out[i] != '"' {
			t.Fatalf("unterminated quote in %q (input %q)", out, in)
		}
		i++
		if i >= len(out) || out[i] != '*' {
			t.Fatalf("expected '*' after closing quote at offset %d of %q (input %q)", i, out, in)
		}
		i++
		// Optional single space, then either another term or end-of-string.
		if i < len(out) {
			if out[i] != ' ' {
				t.Fatalf("expected ' ' or end-of-string at offset %d of %q (input %q)", i, out, in)
			}
			i++
			if i >= len(out) {
				t.Fatalf("trailing space in %q (input %q)", out, in)
			}
		}
	}

	// And: the output must produce the same term count as strings.Fields on the input.
	// (Catches edge cases where the sanitizer drops or duplicates tokens.)
	// Each term is `"…"*` and terms are separated by single ASCII spaces;
	// since the sanitizer only emits ASCII spaces between terms, splitting
	// on ' ' is exact.
	wantTerms := len(strings.FieldsFunc(in, unicode.IsSpace))
	gotTerms := 0
	if out != "" {
		gotTerms = strings.Count(out, " ") + 1
	}
	if wantTerms != gotTerms {
		t.Fatalf("term count mismatch: input %q has %d fields, output %q has %d", in, wantTerms, out, gotTerms)
	}
}
