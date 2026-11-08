package job

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"1mate/internal/ids"
	"1mate/internal/model"
	"1mate/internal/redact"
	"1mate/internal/store"
)

// Handler executes one job. It reports progress through the sink; returning an
// error marks the job failed with that message.
type Handler func(ctx context.Context, j *model.Job, sink *LogSink) error

// StageError carries a machine readable code and a named stage so the UI can
// show a precise failure.
type StageError struct {
	Stage   string
	Code    string
	Message string
}

func (e *StageError) Error() string { return e.Message }

func (e *StageError) Unwrap() error { return nil }

// Manager is the persistent job queue: SQLite holds the state, an in-memory
// worker pool executes handlers, and per-host/per-instance locks serialise
// remote mutations.
type Manager struct {
	store   *store.Store
	dataDir string
	red     *redact.Redactor
	sem     chan struct{}

	handlers map[model.JobKind]Handler

	mu       sync.Mutex
	streams  map[string]*Stream
	cancels  map[string]context.CancelFunc
	hostLock map[string]*sync.Mutex
	instLock map[string]*sync.Mutex

	wg sync.WaitGroup
}

func NewManager(st *store.Store, dataDir string, red *redact.Redactor, workers int) *Manager {
	if workers < 1 {
		workers = 4
	}
	return &Manager{
		store:    st,
		dataDir:  dataDir,
		red:      red,
		sem:      make(chan struct{}, workers),
		handlers: map[model.JobKind]Handler{},
		streams:  map[string]*Stream{},
		cancels:  map[string]context.CancelFunc{},
		hostLock: map[string]*sync.Mutex{},
		instLock: map[string]*sync.Mutex{},
	}
}

func (m *Manager) Register(kind model.JobKind, h Handler) { m.handlers[kind] = h }

// Recover marks jobs left running by a crash and clears stale state.
func (m *Manager) Recover() (int64, error) {
	return m.store.MarkStaleRunningJobs()
}

// Submit persists a queued job and starts it asynchronously.
func (m *Manager) Submit(kind model.JobKind, hostID, instanceID, payload, actor string) (*model.Job, error) {
	if _, ok := m.handlers[kind]; !ok {
		return nil, fmt.Errorf("no handler registered for job kind %s", kind)
	}
	// Guard against duplicate concurrent actions on the same target.
	if instanceID != "" {
		if active, err := m.store.ActiveJobForInstance(instanceID); err == nil && active != nil {
			return nil, ErrConflict
		}
	} else if hostID != "" && kind != model.JobHostProbe {
		if active, err := m.store.ActiveJobForHost(hostID); err == nil && active != nil {
			return nil, ErrConflict
		}
	}
	j := &model.Job{
		ID:         ids.New(),
		Kind:       kind,
		HostID:     hostID,
		InstanceID: instanceID,
		Status:     model.JobQueued,
		Payload:    payload,
		CreatedBy:  actor,
	}
	j.LogPath = filepath.Join(m.dataDir, "jobs", fileNameSafe(string(kind))+"-"+j.ID+".log")
	if err := m.store.CreateJob(j); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.streams[j.ID] = NewStream()
	m.mu.Unlock()
	m.wg.Add(1)
	go m.run(j)
	return j, nil
}

var ErrConflict = errors.New("another job is already running for this target")

func (m *Manager) run(j *model.Job) {
	defer m.wg.Done()
	m.sem <- struct{}{}
	defer func() { <-m.sem }()

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[j.ID] = cancel
	stream := m.streams[j.ID]
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, j.ID)
		m.mu.Unlock()
	}()

	sink, err := newLogSink(j.LogPath, m.red)
	if err != nil {
		_ = m.store.FinishJob(j.ID, model.JobFailed, 1, "cannot open log file: "+err.Error())
		return
	}
	sink.attach(stream)
	defer sink.Close()

	unlock := m.lockFor(j)
	defer unlock()

	if err := m.store.SetJobRunning(j.ID); err != nil {
		return
	}

	h := m.handlers[j.Kind]
	err = h(ctx, j, sink)
	switch {
	case err == nil:
		if serr := m.store.FinishJob(j.ID, model.JobSucceeded, 0, ""); serr == nil {
			sink.Printf("job %s succeeded", j.ID)
		}
	case errors.Is(err, context.Canceled):
		sink.Printf("job %s canceled by user", j.ID)
		_ = m.store.FinishJob(j.ID, model.JobCanceled, 130, "canceled")
	case errors.Is(err, context.DeadlineExceeded):
		sink.Printf("job %s timed out", j.ID)
		_ = m.store.FinishJob(j.ID, model.JobFailed, 124, "timed out")
	default:
		sink.Printf("job %s failed: %s", j.ID, m.red.Apply(err.Error()))
		_ = m.store.FinishJob(j.ID, model.JobFailed, 1, m.red.Apply(err.Error()))
	}
}

func (m *Manager) lockFor(j *model.Job) func() {
	var fns []func()
	if j.HostID != "" {
		fns = append(fns, lockOf(m.hostLock, &m.mu, j.HostID))
	}
	if j.InstanceID != "" {
		fns = append(fns, lockOf(m.instLock, &m.mu, j.InstanceID))
	}
	return func() {
		for i := len(fns) - 1; i >= 0; i-- {
			fns[i]()
		}
	}
}

func lockOf(m map[string]*sync.Mutex, guard *sync.Mutex, key string) func() {
	guard.Lock()
	l, ok := m[key]
	if !ok {
		l = &sync.Mutex{}
		m[key] = l
	}
	guard.Unlock()
	l.Lock()
	return l.Unlock
}

// Cancel requests cancellation of a running job.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	cancel, ok := m.cancels[id]
	m.mu.Unlock()
	if !ok {
		return ErrNotRunning
	}
	cancel()
	return nil
}

var ErrNotRunning = errors.New("job is not running")

// Stream returns the live output stream for a job.
func (m *Manager) Stream(id string) *Stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.streams[id]
	if !ok {
		s = NewStream()
		m.streams[id] = s
		// A finished job has no producer left; mark it closed so SSE ends.
		j, err := m.store.GetJob(id)
		if err == nil && j.Status.Terminal() {
			lines, _ := ReadLogFile(j.LogPath)
			for _, line := range strings.Split(strings.TrimRight(lines, "\n"), "\n") {
				if line != "" {
					s.Publish(line + "\n")
				}
			}
			s.Close()
		}
	}
	return s
}

// Wait blocks until all running jobs settle, used for graceful shutdown.
func (m *Manager) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Shutdown cancels every running job and closes streams.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	for _, c := range m.cancels {
		c()
	}
	streams := make([]*Stream, 0, len(m.streams))
	for _, s := range m.streams {
		streams = append(streams, s)
	}
	m.mu.Unlock()
	for _, s := range streams {
		s.Close()
	}
}

func fileNameSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, strings.ToLower(s))
}
