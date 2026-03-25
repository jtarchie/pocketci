package commands

// Pipeline groups all pipeline management subcommands.
type Pipeline struct {
	Set     SetPipeline     `cmd:"" help:"Upload a pipeline to the server"`
	Rm      DeletePipeline  `cmd:"" help:"Delete a pipeline from the server"`
	Ls      ListPipelines   `cmd:"" help:"List all pipelines on the server"`
	Run     Run             `cmd:"" help:"Run a stored pipeline by name on a server"`
	Trigger TriggerPipeline `cmd:"" help:"Trigger async pipeline execution"`
	Pause   PausePipeline   `cmd:"" help:"Pause a pipeline to prevent new runs"`
	Unpause UnpausePipeline `cmd:"" help:"Unpause a pipeline to allow new runs"`
}
