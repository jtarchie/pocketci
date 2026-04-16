package fly

import (
	"testing"

	fly "github.com/superfly/fly-go"

	"github.com/jtarchie/pocketci/orchestra"
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

func TestBuildInitExec_AbsoluteCachePath(t *testing.T) {
	assert := NewGomegaWithT(t)

	// Absolute cache paths (e.g. /root/.deno, /go/pkg/mod) must be symlinked
	// at their real container path, not under /workspace — otherwise the tool
	// (Deno, Go) writes to the rootfs instead of the persistent volume.
	result := buildInitExec(
		[]string{"sh", "-c", "echo hello"},
		"",
		[]mountMapping{
			{volumeName: "cache-root--deno", mountPath: "/root/.deno"},
		},
	)

	assert.Expect(result).To(HaveLen(3))
	script := result[2]

	// Volume dir must be created in workspace.
	assert.Expect(script).To(ContainSubstring("mkdir -p /workspace/cache-root--deno"))
	// Parent dir at the absolute path.
	assert.Expect(script).To(ContainSubstring("mkdir -p /root"))
	// Symlink at the actual container path, not /workspace/root/.deno.
	assert.Expect(script).To(ContainSubstring("ln -sfn /workspace/cache-root--deno /root/.deno"))
	assert.Expect(script).NotTo(ContainSubstring("/workspace/root/.deno"))
}

func TestBuildInitExec_NoMappings(t *testing.T) {
	assert := NewGomegaWithT(t)

	cmd := []string{"sh", "-c", "echo hello"}
	result := buildInitExec(cmd, "", nil)
	assert.Expect(result).To(Equal(cmd))
}

func TestBuildInitExec_WorkDir(t *testing.T) {
	assert := NewGomegaWithT(t)

	// No mounts: relative workDir resolves to /workspace/<workDir>.
	result := buildInitExec([]string{"sh", "-c", "echo hi"}, "repo", nil)

	assert.Expect(result).To(HaveLen(3))
	assert.Expect(result[2]).To(ContainSubstring("'/workspace/repo'"))
}

func TestBuildInitExec_WorkDirWithMappings(t *testing.T) {
	assert := NewGomegaWithT(t)

	// When both workDir and mappings are set, mount symlinks must be created
	// AND the final cd must use the resolved workDir, not /workspace.
	result := buildInitExec([]string{"bash", "-exc", "task build"}, "repo", []mountMapping{
		{volumeName: "cache-go-pkg-mod", mountPath: "/go/pkg/mod"},
	})

	assert.Expect(result).To(HaveLen(3))
	cmd := result[2]
	assert.Expect(cmd).To(ContainSubstring("mkdir -p /workspace/cache-go-pkg-mod"))
	assert.Expect(cmd).To(ContainSubstring("ln -sfn /workspace/cache-go-pkg-mod /go/pkg/mod"))
	assert.Expect(cmd).To(ContainSubstring("'/workspace/repo'"))
}

func TestExtractExitCode_NoExitEvent(t *testing.T) {
	assert := NewGomegaWithT(t)

	f := &Fly{}
	m := &fly.Machine{
		Events: []*fly.MachineEvent{
			{Type: "start"},
			{Type: "launch"},
		},
	}

	// When no exit event is found, -1 must be returned so that the task is
	// treated as failed (forced-kill / OOM) rather than successful.
	assert.Expect(f.extractExitCode(m)).To(Equal(-1))
}

func TestExtractExitCode_WithExitEvent(t *testing.T) {
	assert := NewGomegaWithT(t)

	f := &Fly{}
	exitCode := 42
	m := &fly.Machine{
		Events: []*fly.MachineEvent{
			{Type: "start"},
			{
				Type: "exit",
				Request: &fly.MachineRequest{
					ExitEvent: &fly.MachineExitEvent{ExitCode: exitCode},
				},
			},
		},
	}

	assert.Expect(f.extractExitCode(m)).To(Equal(exitCode))
}

func TestExtractExitCode_ReturnsLastExitEvent(t *testing.T) {
	assert := NewGomegaWithT(t)

	f := &Fly{}
	m := &fly.Machine{
		Events: []*fly.MachineEvent{
			{
				Type: "exit",
				Request: &fly.MachineRequest{
					ExitEvent: &fly.MachineExitEvent{ExitCode: 1},
				},
			},
			{
				Type: "exit",
				Request: &fly.MachineRequest{
					ExitEvent: &fly.MachineExitEvent{ExitCode: 0},
				},
			},
		},
	}

	// Scans in reverse — the last event (index 1, exit code 0) wins.
	assert.Expect(f.extractExitCode(m)).To(Equal(0))
}

func TestApplyGuestLimits_PerformanceCPUKind(t *testing.T) {
	assert := NewGomegaWithT(t)

	guest := &fly.MachineGuest{CPUKind: "shared", CPUs: 1, MemoryMB: 256}
	applyGuestLimits(guest, orchestra.ContainerLimits{
		CPUKind: "performance",
		CPU:     2,
		Memory:  4 * 1024 * 1024 * 1024, // 4 GB
	})

	assert.Expect(guest.CPUKind).To(Equal("performance"))
	assert.Expect(guest.CPUs).To(Equal(2))
	// 4 GB = 4096 MB — already a multiple of 1024, so no rounding needed
	assert.Expect(guest.MemoryMB).To(Equal(4096))
}

func TestApplyGuestLimits_PerformanceMemoryRounding(t *testing.T) {
	assert := NewGomegaWithT(t)

	// 3 GB = 3072 MB — multiple of 1024, no rounding needed
	guest := &fly.MachineGuest{CPUKind: "shared", CPUs: 1, MemoryMB: 256}
	applyGuestLimits(guest, orchestra.ContainerLimits{
		CPUKind: "performance",
		Memory:  3 * 1024 * 1024 * 1024, // 3 GB
	})
	assert.Expect(guest.MemoryMB).To(Equal(3072))

	// 3.5 GB = 3584 MB — NOT a multiple of 1024 (3584/1024=3.5), round up to 4096
	guest2 := &fly.MachineGuest{CPUKind: "shared", CPUs: 1, MemoryMB: 256}
	applyGuestLimits(guest2, orchestra.ContainerLimits{
		CPUKind: "performance",
		Memory:  int64(3.5 * 1024 * 1024 * 1024),
	})
	assert.Expect(guest2.MemoryMB).To(Equal(4096))
}

func TestApplyGuestLimits_SharedMemoryRounding(t *testing.T) {
	assert := NewGomegaWithT(t)

	// 1 GB = 1024 MB — multiple of 256, no rounding
	guest := &fly.MachineGuest{CPUKind: "shared", CPUs: 1, MemoryMB: 256}
	applyGuestLimits(guest, orchestra.ContainerLimits{
		Memory: 1024 * 1024 * 1024,
	})
	assert.Expect(guest.MemoryMB).To(Equal(1024))

	// 1.1 GB = 1126 MB — round up to next 256 = 1280
	guest2 := &fly.MachineGuest{CPUKind: "shared", CPUs: 1, MemoryMB: 256}
	applyGuestLimits(guest2, orchestra.ContainerLimits{
		Memory: 1181116006, // ~1.1 GB
	})
	assert.Expect(guest2.MemoryMB % 256).To(Equal(0))
	assert.Expect(guest2.MemoryMB).To(BeNumerically(">=", 1126))
}

func TestApplyGuestLimits_SharedAutoUpgrade(t *testing.T) {
	assert := NewGomegaWithT(t)

	// 8 GB on a shared machine should auto-upgrade to 4 CPUs
	guest := &fly.MachineGuest{CPUKind: "shared", CPUs: 1, MemoryMB: 256}
	applyGuestLimits(guest, orchestra.ContainerLimits{
		Memory: 8 * 1024 * 1024 * 1024,
	})
	assert.Expect(guest.CPUs).To(Equal(4))
	assert.Expect(guest.CPUKind).To(Equal("shared"))
}

func TestApplyGuestLimits_PerformanceNoAutoUpgrade(t *testing.T) {
	assert := NewGomegaWithT(t)

	// 8 GB on a performance machine should NOT auto-upgrade CPUs
	guest := &fly.MachineGuest{CPUKind: "shared", CPUs: 1, MemoryMB: 256}
	applyGuestLimits(guest, orchestra.ContainerLimits{
		CPUKind: "performance",
		Memory:  8 * 1024 * 1024 * 1024,
	})
	assert.Expect(guest.CPUs).To(Equal(1))
	assert.Expect(guest.CPUKind).To(Equal("performance"))
}

func TestApplyGuestLimits_NoLimits(t *testing.T) {
	assert := NewGomegaWithT(t)

	// Zero limits should leave guest unchanged
	guest := &fly.MachineGuest{CPUKind: "shared", CPUs: 2, MemoryMB: 512}
	applyGuestLimits(guest, orchestra.ContainerLimits{})
	assert.Expect(guest.CPUKind).To(Equal("shared"))
	assert.Expect(guest.CPUs).To(Equal(2))
	assert.Expect(guest.MemoryMB).To(Equal(512))
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
