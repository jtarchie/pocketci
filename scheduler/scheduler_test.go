package scheduler_test

import (
	"testing"
	"time"

	"github.com/jtarchie/pocketci/scheduler"
	"github.com/jtarchie/pocketci/storage"
	. "github.com/onsi/gomega"
)

func TestComputeNextRun(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)
	now := time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC)

	// Test interval
	next, err := scheduler.ComputeNextRun(storage.ScheduleTypeInterval, "5m", now)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(next).To(Equal(now.Add(5 * time.Minute)))

	// Test cron (every hour at minute 0)
	next, err = scheduler.ComputeNextRun(storage.ScheduleTypeCron, "0 * * * *", now)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(next).To(Equal(time.Date(2026, 3, 29, 13, 0, 0, 0, time.UTC)))

	// Test cron (midnight daily)
	next, err = scheduler.ComputeNextRun(storage.ScheduleTypeCron, "0 0 * * *", now)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(next).To(Equal(time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)))

	// Test invalid interval
	_, err = scheduler.ComputeNextRun(storage.ScheduleTypeInterval, "bad", now)
	assert.Expect(err).To(HaveOccurred())

	// Test invalid cron
	_, err = scheduler.ComputeNextRun(storage.ScheduleTypeCron, "bad", now)
	assert.Expect(err).To(HaveOccurred())

	// Test unknown type
	_, err = scheduler.ComputeNextRun("unknown", "5m", now)
	assert.Expect(err).To(HaveOccurred())
}
