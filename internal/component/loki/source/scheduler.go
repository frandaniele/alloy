package source

import (
	"context"
	"iter"
	"sync"

	"github.com/grafana/dskit/backoff"
)

// Scheduler manages the lifecycle of sources.
// It is not safe for concurrent use: callers must ensure proper synchronization
// when accessing or modifying Scheduler and its sources from multiple goroutines.
type Scheduler[Key comparable] struct {
	ctx     context.Context
	cancel  context.CancelFunc
	sources map[Key]scheduledSource[Key]

	running sync.WaitGroup
}

func NewScheduler[Key comparable]() *Scheduler[Key] {
	ctx, cancel := context.WithCancel(context.Background())

	return &Scheduler[Key]{
		ctx:     ctx,
		cancel:  cancel,
		sources: make(map[Key]scheduledSource[Key]),
	}
}

// ScheduleSource will register and run the provided source in a goroutine.
// If a source with the same key already exists it will do nothing.
func (s *Scheduler[Key]) ScheduleSource(source Source[Key]) {
	k := source.Key()
	if _, ok := s.sources[k]; ok {
		return
	}

	ctx, cancel := context.WithCancel(s.ctx)
	st := scheduledSource[Key]{
		ctx:    ctx,
		cancel: cancel,
		source: source,
	}

	s.sources[k] = st

	s.running.Go(func() {
		st.source.Run(st.ctx)
	})
}

// StopSource unregisters the provided source without waiting for it to stop.
// Drainable sources finish their current work; all other sources are canceled.
func (s *Scheduler[Key]) StopSource(source Source[Key]) {
	k := source.Key()
	scheduledTask, ok := s.sources[k]
	if !ok {
		return
	}
	delete(s.sources, k)
	if source, ok := scheduledTask.source.(DrainableSource[Key]); ok {
		source.StopAndDrain()
		return
	}
	scheduledTask.cancel()
}

// Sources returns an iterator of all scheduled sources.
func (s *Scheduler[Key]) Sources() iter.Seq[Source[Key]] {
	return func(yield func(Source[Key]) bool) {
		for _, scheduledSource := range s.sources {
			if !yield(scheduledSource.source) {
				return
			}
		}
	}
}

// Contains returns true if a source with provided k already exists.
func (s *Scheduler[Key]) Contains(k Key) bool {
	_, ok := s.sources[k]
	return ok
}

// Len returns number of scheduled sources
func (s *Scheduler[Key]) Len() int {
	return len(s.sources)
}

// Stop will stop all running sources and wait for them to finish.
// Scheduler should not be reused after Stop is called.
func (s *Scheduler[Key]) Stop() {
	s.cancel()
	s.running.Wait()
	s.sources = make(map[Key]scheduledSource[Key])
}

// Reset will stop all running sources and wait for them to finish and reset
// Scheduler to a usable state.
func (s *Scheduler[Key]) Reset() {
	s.cancel()
	s.running.Wait()
	s.sources = make(map[Key]scheduledSource[Key])
	s.ctx, s.cancel = context.WithCancel(context.Background())
}

type Source[Key comparable] interface {
	// Run should start the source.
	// It should run until there is no more work or context is canceled.
	Run(ctx context.Context)
	// Key is used to uniquely identify the source.
	Key() Key
}

// DrainableSource is a source that can stop after finishing its current work.
// StopAndDrain must not block. The source still needs to stop immediately when
// the context passed to Run is canceled.
type DrainableSource[Key comparable] interface {
	Source[Key]
	StopAndDrain()
}

// DebugSource is an optional interface with debug information.
type DebugSource interface {
	DebugInfo() any
}

func NewSourceWithRetry[Key comparable](source Source[Key], config backoff.Config) *SourceWithRetry[Key] {
	drainCtx, stopAndDrain := context.WithCancel(context.Background())
	return &SourceWithRetry[Key]{
		source:       source,
		config:       config,
		drainCtx:     drainCtx,
		stopAndDrain: stopAndDrain,
	}
}

// SourceWithRetry is used to wrap another source and apply retries
// when running.
type SourceWithRetry[Key comparable] struct {
	source       Source[Key]
	config       backoff.Config
	drainCtx     context.Context
	stopAndDrain context.CancelFunc
}

func (s *SourceWithRetry[Key]) Run(ctx context.Context) {
	retryCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.drainCtx, cancel)
	defer func() {
		stop()
		cancel()
	}()

	backoff := backoff.New(retryCtx, s.config)

	for backoff.Ongoing() {
		select {
		case <-s.drainCtx.Done():
			return
		default:
		}

		if _, ok := s.source.(DrainableSource[Key]); ok {
			s.source.Run(ctx)
		} else {
			s.source.Run(retryCtx)
		}

		select {
		case <-s.drainCtx.Done():
			return
		default:
		}
		backoff.Wait()
	}
}

func (s *SourceWithRetry[Key]) StopAndDrain() {
	if source, ok := s.source.(DrainableSource[Key]); ok {
		source.StopAndDrain()
	}
	s.stopAndDrain()
}

func (s *SourceWithRetry[Key]) Key() Key {
	return s.source.Key()
}

func (s *SourceWithRetry[Key]) DebugInfo() any {
	ss, ok := s.source.(DebugSource)
	if !ok {
		return nil
	}
	return ss.DebugInfo()
}

// scheduledSource is a source that is already scheduled.
// to stop the scheduledSource cancel needs to be called.
type scheduledSource[Key comparable] struct {
	ctx    context.Context
	cancel context.CancelFunc
	source Source[Key]
}
