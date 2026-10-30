package source

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/grafana/dskit/backoff"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/goleak"
)

var _ Source[int] = (*testSource)(nil)
var _ DrainableSource[int] = (*testDrainableSource)(nil)

func newTestSource(key int, exit bool) *testSource {
	return &testSource{
		key:        key,
		shouldExit: exit,
		running:    *atomic.NewBool(false),
	}
}

type testSource struct {
	key        int
	shouldExit bool
	numStarts  atomic.Int32
	running    atomic.Bool
}

func (t *testSource) Run(ctx context.Context) {
	t.numStarts.Inc()
	t.running.Store(true)
	defer t.running.Store(false)

	if t.shouldExit {
		return
	}
	<-ctx.Done()
}

func (t *testSource) Key() int {
	return t.key
}

func (t *testSource) IsRunning() bool {
	return t.running.Load()
}

type testDrainableSource struct {
	key       int
	running   atomic.Bool
	drained   atomic.Bool
	aborted   atomic.Bool
	drain     chan struct{}
	drainOnce sync.Once
}

func newTestDrainableSource(key int) *testDrainableSource {
	return &testDrainableSource{key: key, drain: make(chan struct{})}
}

func (t *testDrainableSource) Run(ctx context.Context) {
	t.running.Store(true)
	defer t.running.Store(false)
	select {
	case <-t.drain:
		t.drained.Store(true)
	case <-ctx.Done():
		t.aborted.Store(true)
	}
}

func (t *testDrainableSource) Key() int { return t.key }

func (t *testDrainableSource) StopAndDrain() {
	t.drainOnce.Do(func() { close(t.drain) })
}

func TestScheduler(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"))

	t.Run("should ignore duplicated keys", func(t *testing.T) {
		scheduler := NewScheduler[int]()
		scheduler.ScheduleSource(newTestSource(1, false))
		scheduler.ScheduleSource(newTestSource(1, false))
		require.Equal(t, 1, scheduler.Len())
		scheduler.Stop()
		require.Equal(t, 0, scheduler.Len())
	})

	t.Run("should stop running source", func(t *testing.T) {
		scheduler := NewScheduler[int]()
		source1, source2 := newTestSource(1, false), newTestSource(2, false)
		scheduler.ScheduleSource(source1)
		scheduler.ScheduleSource(source2)
		require.Equal(t, 2, scheduler.Len())

		scheduler.StopSource(source1)
		require.Eventually(t, func() bool {
			return !source1.IsRunning()
		}, 3*time.Second, 10*time.Millisecond)

		require.Equal(t, 1, scheduler.Len())
		require.True(t, scheduler.Contains(source2.Key()))

		scheduler.Stop()
		require.Equal(t, 0, scheduler.Len())
	})

	t.Run("source that stops", func(t *testing.T) {
		scheduler := NewScheduler[int]()
		s := newTestSource(1, true)
		scheduler.ScheduleSource(s)

		require.Eventually(t, func() bool {
			return !s.IsRunning()
		}, 3*time.Second, 10*time.Millisecond)

		scheduler.Stop()
	})

	t.Run("target removal drains a drainable source", func(t *testing.T) {
		scheduler := NewScheduler[int]()
		s := newTestDrainableSource(1)
		scheduler.ScheduleSource(s)
		require.Eventually(t, s.running.Load, time.Second, 10*time.Millisecond)

		scheduler.StopSource(s)
		require.Eventually(t, func() bool { return !s.running.Load() }, time.Second, 10*time.Millisecond)
		require.True(t, s.drained.Load())
		require.False(t, s.aborted.Load())
		scheduler.Stop()
	})

	t.Run("scheduler shutdown aborts a drainable source", func(t *testing.T) {
		scheduler := NewScheduler[int]()
		s := newTestDrainableSource(1)
		scheduler.ScheduleSource(s)
		require.Eventually(t, s.running.Load, time.Second, 10*time.Millisecond)

		scheduler.Stop()
		require.True(t, s.aborted.Load())
		require.False(t, s.drained.Load())
	})
}

func TestScheduler_SourceWithRetry(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"))

	scheduler := NewScheduler[int]()
	s := newTestSource(1, true)

	scheduler.ScheduleSource(NewSourceWithRetry(s, backoff.Config{
		MinBackoff: 20 * time.Millisecond,
		MaxBackoff: 10 * time.Second,
	}))

	time.Sleep(1 * time.Second)
	require.True(t, s.numStarts.Load() > 1)
	scheduler.Stop()
}

func TestScheduler_SourceWithRetry_TargetRemovalStopsNonDrainableSource(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"))

	scheduler := NewScheduler[int]()
	s := newTestSource(1, false)
	retrying := NewSourceWithRetry(s, backoff.Config{
		MinBackoff: 20 * time.Millisecond,
		MaxBackoff: time.Second,
	})
	scheduler.ScheduleSource(retrying)
	require.Eventually(t, s.IsRunning, time.Second, 10*time.Millisecond)

	scheduler.StopSource(retrying)
	require.Eventually(t, func() bool { return !s.IsRunning() }, time.Second, 10*time.Millisecond)
	scheduler.Stop()
}
