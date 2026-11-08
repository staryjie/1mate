package job

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"1mate/internal/redact"
)

// LogSink writes redacted job output to a file and mirrors it to live
// subscribers (SSE).
type LogSink struct {
	mu     sync.Mutex
	file   *os.File
	stream *Stream
	red    *redact.Redactor
	bytes  int64
}

const maxLogBytes = 20 << 20

func newLogSink(path string, red *redact.Redactor) (*LogSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &LogSink{file: f, red: red}, nil
}

func (l *LogSink) attach(stream *Stream) { l.stream = stream }

// Printf writes a formatted, redacted line.
func (l *LogSink) Printf(format string, args ...any) {
	l.Write(format + "\n")
}

// Write appends text, splitting it into lines so the SSE client receives
// complete chunks.
func (l *LogSink) Write(text string) {
	if text == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bytes >= maxLogBytes {
		return
	}
	if l.red != nil {
		text = l.red.Apply(text)
	}
	n, err := l.file.WriteString(text)
	if err == nil {
		l.bytes += int64(n)
		if l.stream != nil {
			l.stream.Publish(text)
		}
	}
}

// Stage records a named phase so the UI can show a progress bar.
func (l *LogSink) Stage(name, message string) {
	l.Write("== " + name + " == " + message + "\n")
}

func (l *LogSink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	if l.stream != nil {
		l.stream.Close()
		l.stream = nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Stream is an in-memory ring buffer plus pub/sub used by SSE handlers.
type Stream struct {
	mu     sync.Mutex
	lines  []string
	limit  int
	subs   map[chan string]struct{}
	closed bool
}

func NewStream() *Stream {
	return &Stream{limit: 4000, subs: map[chan string]struct{}{}}
}

func (s *Stream) Publish(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lines = append(s.lines, text)
	if len(s.lines) > s.limit {
		s.lines = s.lines[len(s.lines)-s.limit:]
	}
	for ch := range s.subs {
		select {
		case ch <- text:
		default:
			// A slow consumer must never block the job itself.
		}
	}
}

func (s *Stream) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for ch := range s.subs {
		close(ch)
		delete(s.subs, ch)
	}
}

func (s *Stream) Snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.lines))
	copy(out, s.lines)
	return out
}

func (s *Stream) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 256)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

// ReadLogFile returns the persisted log content, capped at maxLogBytes.
func ReadLogFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteString("\n")
	}
	return b.String(), sc.Err()
}
