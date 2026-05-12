package server

import (
	"context"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/backwards"
	"github.com/jtarchie/pocketci/storage"
)

// JobSummary is the view model for one row of the pipeline detail page's
// Jobs section. It conveys the job's trigger configuration, the most
// recent run (any status, plus a clickable link), and — for triggers.passed
// jobs — which upstreams still need a fresh success.
type JobSummary struct {
	Name              string
	TriggerChips      []TriggerChip
	LatestRun         *storage.PipelineRun
	WaitingOn         []string // triggers.passed upstreams that haven't succeeded since this job's last run
	ReadyToFire       bool     // true when all triggers.passed upstreams have a fresh success
	HasTriggersPassed bool
}

// TriggerChip describes a single chip rendered in the Jobs section. Kind
// drives the icon + color recipe; Detail is the human-readable specifics
// (cron expression, filter expr, upstream list).
type TriggerChip struct {
	Kind   string // "webhook" | "schedule" | "passed" | "manual"
	Detail string
}

// buildJobSummaries parses the pipeline's YAML content (if any), then for
// each job loads the latest run, its trigger chips, and — for triggers.passed
// jobs — the freshness status. Returns nil for non-YAML pipelines.
func buildJobSummaries(ctx context.Context, store storage.Driver, pipeline *storage.Pipeline) []JobSummary {
	if pipeline == nil || pipeline.ContentType != storage.ContentTypeYAML {
		return nil
	}

	cfg, err := backwards.ParseConfig(pipeline.Content)
	if err != nil {
		return nil
	}

	summaries := make([]JobSummary, 0, len(cfg.Jobs))
	for i := range cfg.Jobs {
		job := &cfg.Jobs[i]
		summary := JobSummary{
			Name:         job.Name,
			TriggerChips: buildTriggerChips(job),
		}

		latest, lookupErr := store.GetMostRecentJobRun(ctx, pipeline.ID, job.Name)
		if lookupErr == nil && latest != nil {
			summary.LatestRun = latest
		}

		if job.Triggers != nil && len(job.Triggers.Passed) > 0 {
			summary.HasTriggersPassed = true
			summary.WaitingOn, summary.ReadyToFire = evaluateFreshness(ctx, store, pipeline.ID, job, latest)
		}

		summaries = append(summaries, summary)
	}

	return summaries
}

func buildTriggerChips(job *backwards.Job) []TriggerChip {
	if job.Triggers == nil {
		return []TriggerChip{{Kind: "manual"}}
	}

	chips := make([]TriggerChip, 0, 3)

	if job.Triggers.Webhook != nil {
		detail := ""
		if job.Triggers.Webhook.Filter != "" {
			detail = "filter: " + job.Triggers.Webhook.Filter
		}

		chips = append(chips, TriggerChip{Kind: "webhook", Detail: detail})
	}

	if job.Triggers.Schedule != nil {
		detail := ""

		switch {
		case job.Triggers.Schedule.Cron != "":
			detail = "cron: " + job.Triggers.Schedule.Cron
		case job.Triggers.Schedule.Every != "":
			detail = "every: " + job.Triggers.Schedule.Every
		}

		chips = append(chips, TriggerChip{Kind: "schedule", Detail: detail})
	}

	if len(job.Triggers.Passed) > 0 {
		chips = append(chips, TriggerChip{Kind: "passed", Detail: joinNames(job.Triggers.Passed)})
	}

	if len(chips) == 0 {
		// Triggers block present but empty — explicit manual-only.
		chips = append(chips, TriggerChip{Kind: "manual"})
	}

	return chips
}

func joinNames(names []string) string {
	return strings.Join(names, ", ")
}

// evaluateFreshness mirrors the scanner's freshness check (without firing).
// Returns the list of upstreams that have NOT had a successful run since
// the downstream's last run; readyToFire is true when the list is empty.
func evaluateFreshness(ctx context.Context, store storage.Driver, pipelineID string, job *backwards.Job, latest *storage.PipelineRun) ([]string, bool) {
	since := time.Time{}
	if latest != nil {
		since = latest.CreatedAt
	}

	var waiting []string

	for _, upstream := range job.Triggers.Passed {
		fresh, err := store.GetSuccessfulJobRunSince(ctx, pipelineID, upstream, since)
		if err != nil || fresh == nil {
			waiting = append(waiting, upstream)
		}
	}

	return waiting, len(waiting) == 0
}
