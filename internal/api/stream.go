package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"1mate/internal/model"
)

// handleJobEvents streams job output as Server-Sent Events. It replays the
// buffered log first so a late subscriber does not miss the beginning.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.store.GetJob(id)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, http.StatusInternalServerError, CodeInternal, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	stream := s.jobs.Stream(id)
	lastEventID := r.Header.Get("Last-Event-ID")

	seq := 0
	send := func(text string) bool {
		seq++
		payload, _ := json.Marshal(map[string]string{"text": s.svc.Red.Apply(text)})
		if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	_ = lastEventID

	for _, line := range stream.Snapshot() {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !send(line) {
			return
		}
	}

	ch, cancel := stream.Subscribe()
	defer cancel()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	terminal := j.Status.Terminal()
	if terminal && stream.Closed() {
		sendStatus(w, flusher, j)
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				fresh, err := s.store.GetJob(id)
				if err == nil {
					sendStatus(w, flusher, fresh)
				}
				return
			}
			if !send(line) {
				return
			}
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
			fresh, err := s.store.GetJob(id)
			if err == nil && fresh.Status.Terminal() && stream.Closed() {
				sendStatus(w, flusher, fresh)
				return
			}
		}
	}
}

func sendStatus(w http.ResponseWriter, flusher http.Flusher, j *model.Job) {
	b, _ := json.Marshal(j)
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", b)
	flusher.Flush()
}

// handleStreamInstanceLogs tails the compose logs for a short window. It is a
// bounded snapshot because a full tail would hold the connection forever.
func (s *Server) handleInstanceLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst, err := s.svc.GetInstance(id)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "200"
	}
	switch tail {
	case "50", "100", "200", "500", "1000":
	default:
		tail = "200"
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	res, err := s.svc.RunCompose(ctx, inst, "logs --no-color --tail "+tail, 45*time.Second, nil)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"logs":      s.svc.Red.Apply(res.Stdout),
		"exit_code": res.ExitCode,
	})
}
