package fly

import (
	"testing"

	. "github.com/onsi/gomega"
)

// TestBuildInitExec_SubdirCachePath is a regression test for the bug where a
// cache path with a subdirectory component (e.g. "repo/.git") caused exit 1
// because buildInitExec issued:
//
//	ln -sfn /workspace/cache-repo--git /workspace/repo/.git
//
// without first creating /workspace/repo/, so ln failed and the &&-chained
// init script exited 1 before the task command ran.
func TestBuildInitExec_SubdirCachePath(t *testing.T) {
	assert := NewGomegaWithT(t)

	// "repo/.git" is the exact cache path from the pocketci-ci pipeline checkout task.
	result := buildInitExec(
		[]string{"sh", "-c", "echo hello"},
		"",
		[]mountMapping{
			{volumeName: "cache-repo--git", mountPath: "repo/.git"},
		},
	)

	assert.Expect(result).To(HaveLen(3))
	script := result[2]

	// Parent directory must be created before the symlink is attempted.
	assert.Expect(script).To(ContainSubstring("mkdir -p /workspace/repo"))
	assert.Expect(script).To(ContainSubstring("ln -sfn /workspace/cache-repo--git /workspace/repo/.git"))

	// mkdir of parent must appear before ln in the script.
	mkdirPos := indexOfSubstring(script, "mkdir -p /workspace/repo")
	lnPos := indexOfSubstring(script, "ln -sfn /workspace/cache-repo--git")
	assert.Expect(mkdirPos).To(BeNumerically("<", lnPos), "mkdir -p of parent dir must precede ln")
}

func TestBuildInitExec_FlatCachePath(t *testing.T) {
	assert := NewGomegaWithT(t)

	// Flat cache paths (e.g. "cache") have parent /workspace which already exists.
	// The symlink is still needed because volumeName ("cache-cache") != mountPath ("cache").
	result := buildInitExec(
		[]string{"sh", "-c", "echo hello"},
		"",
		[]mountMapping{
			{volumeName: "cache-cache", mountPath: "cache"},
		},
	)

	assert.Expect(result).To(HaveLen(3))
	script := result[2]

	// The volume dir and symlink must both be present.
	assert.Expect(script).To(ContainSubstring("mkdir -p /workspace/cache-cache"))
	assert.Expect(script).To(ContainSubstring("ln -sfn /workspace/cache-cache /workspace/cache"))
}

func TestBuildInitExec_NoMappings(t *testing.T) {
	assert := NewGomegaWithT(t)

	cmd := []string{"sh", "-c", "echo hello"}
	result := buildInitExec(cmd, "", nil)
	assert.Expect(result).To(Equal(cmd))
}

func TestBuildInitExec_WorkDir(t *testing.T) {
	assert := NewGomegaWithT(t)

	result := buildInitExec([]string{"sh", "-c", "echo hi"}, "repo", []mountMapping{
		{volumeName: "cache-repo--git", mountPath: "repo/.git"},
	})

	// workDir takes precedence — mappings are ignored.
	assert.Expect(result).To(HaveLen(3))
	assert.Expect(result[2]).To(ContainSubstring("cd 'repo'"))
	assert.Expect(result[2]).NotTo(ContainSubstring("mkdir -p"))
}

// indexOfSubstring returns the byte position of needle in s, or -1.
func indexOfSubstring(s, needle string) int {
	for i := range s {
		if len(s[i:]) >= len(needle) && s[i:i+len(needle)] == needle {
			return i
		}
	}

	return -1
}
