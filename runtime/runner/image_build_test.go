package runner

import (
	"strings"
	"testing"

	"github.com/onsi/gomega"
)

func TestRegistryFromTag(t *testing.T) {
	t.Parallel()

	cases := []struct {
		tag      string
		expected string
	}{
		{"alpine:latest", "docker.io"},
		{"library/alpine:latest", "docker.io"},
		{"localhost:5000/foo:bar", "localhost:5000"},
		{"registry.example.com/foo:bar", "registry.example.com"},
		{"registry.example.com:5000/foo/bar:baz", "registry.example.com:5000"},
		{"ghcr.io/jtarchie/pocketci:latest", "ghcr.io"},
	}

	for _, tc := range cases {
		got := registryFromTag(tc.tag)
		if got != tc.expected {
			t.Errorf("registryFromTag(%q) = %q, want %q", tc.tag, got, tc.expected)
		}
	}
}

func TestExtractBuildImageDigest(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	stdout := strings.Join([]string{
		"some build noise",
		"POCKETCI_IMAGE_METADATA_BEGIN",
		`{"containerimage.config.digest":"sha256:abcd","containerimage.digest":"sha256:c0ffee"}`,
		"POCKETCI_IMAGE_METADATA_END",
		"trailing noise",
	}, "\n")

	g.Expect(extractBuildImageDigest(stdout)).To(gomega.Equal("sha256:c0ffee"))
	g.Expect(extractBuildImageDigest("nothing here")).To(gomega.BeEmpty())
}

func TestBuildImageScriptIncludesAuthAndCommand(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	input := BuildImageInput{
		Tag:        "registry.example.com/myapp:latest",
		Context:    "source",
		Dockerfile: "Dockerfile",
		Push:       true,
		BuildArgs: map[string]string{
			"GO_VERSION": "1.26",
		},
		Target: "production",
		RegistryAuth: &BuildImageRegistryAuth{
			Username: "user",
			Password: "pass",
		},
	}

	script := buildImageScript(input, "./source", "./source", "Dockerfile", "registry.example.com")

	g.Expect(script).To(gomega.ContainSubstring("buildctl-daemonless.sh build"))
	g.Expect(script).To(gomega.ContainSubstring("--frontend dockerfile.v0"))
	g.Expect(script).To(gomega.ContainSubstring("--local context='./source'"))
	g.Expect(script).To(gomega.ContainSubstring("--local dockerfile='./source'"))
	g.Expect(script).To(gomega.ContainSubstring("--opt filename='Dockerfile'"))
	g.Expect(script).To(gomega.ContainSubstring(
		"--output 'type=image,name=registry.example.com/myapp:latest,push=true'",
	))
	g.Expect(script).To(gomega.ContainSubstring("--opt target='production'"))
	g.Expect(script).To(gomega.ContainSubstring("--opt 'build-arg:GO_VERSION=1.26'"))
	g.Expect(script).To(gomega.ContainSubstring("--metadata-file /tmp/pocketci-buildkit-meta.json"))
	g.Expect(script).To(gomega.ContainSubstring(`/root/.docker/config.json`))
	g.Expect(script).To(gomega.ContainSubstring(`"registry.example.com"`))
}

func TestBuildImageScriptNoAuth(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	input := BuildImageInput{
		Tag:        "localhost:5000/foo:bar",
		Context:    "source",
		Dockerfile: "Dockerfile",
		Push:       false,
	}

	script := buildImageScript(input, "./source", "./source", "Dockerfile", "localhost:5000")

	g.Expect(script).NotTo(gomega.ContainSubstring("/root/.docker/config.json"))
	g.Expect(script).To(gomega.ContainSubstring("--output 'type=image,name=localhost:5000/foo:bar,push=false'"))
}

func TestBuildImageScriptShellQuoting(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	input := BuildImageInput{
		Tag:     "localhost:5000/foo:bar",
		Context: "src dir",
		BuildArgs: map[string]string{
			"WITH_QUOTE": "it's fine",
		},
	}

	script := buildImageScript(input, "./src dir", "./src dir", "Dockerfile", "localhost:5000")

	// Single-quoted strings escape inner quotes as '\''
	g.Expect(script).To(gomega.ContainSubstring(`--local context='./src dir'`))
	g.Expect(script).To(gomega.ContainSubstring(`--opt 'build-arg:WITH_QUOTE=it'\''s fine'`))
}

func TestValidateBuildImageInput(t *testing.T) {
	t.Parallel()

	g := gomega.NewWithT(t)

	g.Expect(validateBuildImageInput(BuildImageInput{Context: "src"})).To(gomega.MatchError(gomega.ContainSubstring("tag is required")))
	g.Expect(validateBuildImageInput(BuildImageInput{Tag: "x:y"})).To(gomega.MatchError(gomega.ContainSubstring("context is required")))
	g.Expect(validateBuildImageInput(BuildImageInput{Tag: "x y", Context: "."})).To(gomega.MatchError(gomega.ContainSubstring("contains whitespace")))
	g.Expect(validateBuildImageInput(BuildImageInput{Tag: "x:y", Context: "."})).To(gomega.Succeed())
}
