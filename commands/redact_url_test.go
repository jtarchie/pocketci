package commands_test

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jtarchie/pocketci/commands"
)

func TestRedactURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, input, expected string
	}{
		{"no credentials", "http://example.com/api", "http://example.com/api"},
		{"username only", "http://admin@example.com/api", "http://admin@example.com/api"},
		{"username and password", "http://admin:secret@example.com/api", "http://admin:xxxxx@example.com/api"},
		{"invalid url", "://not-a-url", "://not-a-url"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert := NewGomegaWithT(t)
			assert.Expect(commands.RedactURL(tc.input)).To(Equal(tc.expected))
		})
	}
}
