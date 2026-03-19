package storage_test

import (
	"testing"

	. "github.com/onsi/gomega"

	storage "github.com/jtarchie/pocketci/storage/sqlite"
)

func TestStripANSI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "SGR reset",
			input: "\x1b[0mhello\x1b[0m",
			want:  "hello",
		},
		{
			name:  "bold green text",
			input: "\x1b[1;32mBUILD OK\x1b[0m",
			want:  "BUILD OK",
		},
		{
			name:  "256-color foreground",
			input: "\x1b[38;5;200mcolored\x1b[0m",
			want:  "colored",
		},
		{
			name:  "OSC with BEL terminator",
			input: "\x1b]0;window title\x07plain",
			want:  "plain",
		},
		{
			name:  "OSC with ST terminator",
			input: "\x1b]0;title\x1b\\plain",
			want:  "plain",
		},
		{
			name:  "simple 2-char ESC sequence strips ESC plus next char",
			input: "\x1bMtext",
			want:  "text",
		},
		{
			name:  "lone ESC at end of string",
			input: "text\x1b",
			want:  "text",
		},
		{
			name:  "mixed content",
			input: "\x1b[32mPASSED\x1b[0m: 42 tests",
			want:  "PASSED: 42 tests",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "hyperlink OSC 8",
			input: "\x1b]8;;https://example.com\x07linktext\x1b]8;;\x07",
			want:  "linktext",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			got := storage.StripANSI(tc.input)
			assert.Expect(got).To(Equal(tc.want))
		})
	}
}
