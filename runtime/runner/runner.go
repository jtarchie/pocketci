package runner

import (
	"encoding/json"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/secrets"
)

// Runner is the interface for running pipeline steps.
// Both PipelineRunner and ResumableRunner implement this interface.
type Runner interface {
	Run(input RunInput) (*RunResult, error)
	CreateVolume(input VolumeInput) (*VolumeResult, error)
	CleanupVolumes() error
	// StartSandbox starts a long-lived sandbox container for multi-command execution.
	// Returns an error if the underlying driver does not support sandbox mode.
	StartSandbox(input SandboxInput) (*SandboxHandle, error)
	// SetAgentFunc configures the function used to execute agent steps.
	// Must be called before RunAgent.
	SetAgentFunc(fn AgentFunc)
	// RunAgent executes an LLM agent step. The config is passed as raw JSON
	// to avoid importing the agent package. Returns the result as raw JSON.
	RunAgent(configJSON json.RawMessage) (json.RawMessage, error)
	// ReadFilesFromVolume reads specific files from a volume and returns
	// their contents as a map of relative path to file content.
	ReadFilesFromVolume(volumeName string, filePaths ...string) (map[string]string, error)
	SetSecretsManager(mgr secrets.Manager, pipelineID string)
	SetPreseededVolumes(vols map[string]orchestra.Volume)
	SetOutputCallback(cb OutputCallback)
}

// Ensure both runners implement the interface.
var (
	_ Runner = (*PipelineRunner)(nil)
	_ Runner = (*ResumableRunner)(nil)
)
