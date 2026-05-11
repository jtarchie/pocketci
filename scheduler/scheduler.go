package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jtarchie/pocketci/storage"
	"github.com/robfig/cron/v3"
)

// TriggerFunc is called when a schedule fires.
type TriggerFunc func(ctx context.Context, schedule storage.Schedule) error

// Metrics is the recording surface used by the scheduler. Same shape as
// observability.Metrics; defined locally so the scheduler package does not
// take an import on a metrics backend.
type Metrics interface {
	CounterAdd(name string, delta float64, attributes map[string]string)
	GaugeSet(name string, value float64, attributes map[string]string)
	HistogramObserve(name string, value float64, attributes map[string]string)
}

type noopMetrics struct{}

func (noopMetrics) CounterAdd(_ string, _ float64, _ map[string]string)       {}
func (noopMetrics) GaugeSet(_ string, _ float64, _ map[string]string)         {}
func (noopMetrics) HistogramObserve(_ string, _ float64, _ map[string]string) {}

// Scheduler is a background service that periodically checks for due schedules
// and triggers pipeline runs via the provided TriggerFunc.
type Scheduler struct {
	store   storage.Driver
	trigger TriggerFunc
	logger  *slog.Logger
	ticker  *time.Ticker
	done    chan struct{}
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	metrics Metrics
}

// New creates a new Scheduler. The trigger function is called for each due
// schedule. The tick interval controls how often the scheduler polls for due
// schedules.
func New(store storage.Driver, trigger TriggerFunc, logger *slog.Logger, tickInterval time.Duration) *Scheduler {
	return &Scheduler{
		store:   store,
		trigger: trigger,
		logger:  logger,
		ticker:  time.NewTicker(tickInterval),
		done:    make(chan struct{}),
		cancel:  func() {}, // replaced in Start
		metrics: noopMetrics{},
	}
}

// SetMetrics swaps the recording surface. Must be called before Start to avoid
// racing with the scheduler goroutine.
func (s *Scheduler) SetMetrics(m Metrics) {
	if m == nil {
		s.metrics = noopMetrics{}

		return
	}

	s.metrics = m
}

// Start launches the background scheduler goroutine.
func (s *Scheduler) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

// Stop signals the scheduler to stop and waits for it to finish.
func (s *Scheduler) Stop() {
	close(s.done)
	s.cancel()
	s.ticker.Stop()
	s.wg.Wait()
}

func (s *Scheduler) run(ctx context.Context) {
	for {
		select {
		case <-s.done:
			return
		case <-s.ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now().UTC()

	s.metrics.CounterAdd("pocketci_scheduler_tick_total", 1, nil)

	schedules, err := s.store.ClaimDueSchedules(ctx, now)
	if err != nil {
		s.logger.Error("scheduler.claim.failed", slog.String("error", err.Error()))
		s.metrics.CounterAdd("pocketci_scheduler_claim_total", 1, map[string]string{"result": "error"})

		return
	}

	s.metrics.CounterAdd("pocketci_scheduler_claim_total", 1, map[string]string{"result": "ok"})
	s.metrics.GaugeSet("pocketci_scheduler_due_schedules", float64(len(schedules)), nil)

	for _, sched := range schedules {
		s.processSchedule(ctx, now, sched)
	}
}

func (s *Scheduler) processSchedule(ctx context.Context, now time.Time, sched storage.Schedule) {
	logger := s.logger.With(
		slog.String("schedule_id", sched.ID),
		slog.String("pipeline_id", sched.PipelineID),
		slog.String("schedule_name", sched.Name),
	)

	err := s.trigger(ctx, sched)

	result := "success"
	if err != nil {
		logger.Error("scheduler.trigger.failed", slog.String("error", err.Error()))
		result = "failure"
	} else {
		logger.Info("scheduler.trigger.success")
	}

	s.metrics.CounterAdd("pocketci_scheduler_trigger_total", 1, map[string]string{"result": result})

	nextRunAt, err := ComputeNextRun(sched.ScheduleType, sched.ScheduleExpr, now)
	if err != nil {
		logger.Error("scheduler.next_run.failed", slog.String("error", err.Error()))

		return
	}

	err = s.store.UpdateScheduleAfterRun(ctx, sched.ID, now, nextRunAt)
	if err != nil {
		logger.Error("scheduler.update.failed", slog.String("error", err.Error()))
	}
}

// ComputeNextRun calculates the next run time for a schedule based on its type and expression.
func ComputeNextRun(schedType storage.ScheduleType, expr string, from time.Time) (time.Time, error) {
	switch schedType {
	case storage.ScheduleTypeCron:
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

		schedule, err := parser.Parse(expr)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid cron expression %q: %w", expr, err)
		}

		return schedule.Next(from), nil
	case storage.ScheduleTypeInterval:
		duration, err := time.ParseDuration(expr)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid interval %q: %w", expr, err)
		}

		return from.Add(duration), nil
	default:
		return time.Time{}, fmt.Errorf("unknown schedule type %q", schedType)
	}
}
