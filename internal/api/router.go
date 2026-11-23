package api

import (
	"bufio"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"
	"time"
)

// Router builds the full HTTP surface: API, SSE, health and the embedded SPA.
func Router(s *Server, webFS fs.FS, corsAllowed string) http.Handler {
	mux := http.NewServeMux()

	api := func(method, pattern string, h http.HandlerFunc, public bool) {
		handler := http.Handler(h)
		if !public {
			handler = s.requireAuth(handler)
		}
		mux.Handle(method+" "+pattern, handler)
	}

	// Public auth endpoints.
	api(http.MethodGet, "/api/v1/auth/setup", s.handleSetupStatus, true)
	api(http.MethodPost, "/api/v1/auth/setup", s.handleSetup, true)
	api(http.MethodPost, "/api/v1/auth/login", s.handleLogin, true)
	api(http.MethodPost, "/api/v1/auth/logout", s.handleLogout, true)
	api(http.MethodGet, "/api/v1/health", s.handleHealth, true)

	// Authenticated session info.
	api(http.MethodGet, "/api/v1/auth/me", s.handleMe, false)
	api(http.MethodPost, "/api/v1/auth/change-password", s.handleChangePassword, false)

	// Hosts.
	api(http.MethodGet, "/api/v1/hosts", s.handleListHosts, false)
	api(http.MethodPost, "/api/v1/hosts", s.handleCreateHost, false)
	api(http.MethodGet, "/api/v1/hosts/{id}", s.handleGetHost, false)
	api(http.MethodPatch, "/api/v1/hosts/{id}", s.handleUpdateHost, false)
	api(http.MethodDelete, "/api/v1/hosts/{id}", s.handleDeleteHost, false)
	api(http.MethodPost, "/api/v1/hosts/{id}/actions/probe", s.handleProbeHost, false)
	api(http.MethodPost, "/api/v1/hosts/test-connection", s.handleTestConnection, false)
	api(http.MethodGet, "/api/v1/hosts/{id}/capabilities", s.handleCapabilities, false)
	api(http.MethodGet, "/api/v1/hosts/{id}/install-script", s.handleInstallScript, false)
	api(http.MethodPost, "/api/v1/hosts/{id}/actions/install-docker", s.handleInstall, false)

	// 镜像预检与主机间同步。
	api(http.MethodPost, "/api/v1/hosts/{id}/images/precheck", s.handleImagePrecheck, false)
	api(http.MethodPost, "/api/v1/hosts/{id}/images/sync", s.handleImageSync, false)

	// 主机终端 (WebSocket)。
	mux.Handle("GET /api/v1/hosts/{id}/terminal", s.requireAuth(http.HandlerFunc(s.handleHostTerminal)))

	// Instances.
	api(http.MethodGet, "/api/v1/instances", s.handleListInstances, false)
	api(http.MethodPost, "/api/v1/instances", s.handleCreateInstance, false)
	api(http.MethodPost, "/api/v1/instances/import", s.handleImportInstance, false)
	api(http.MethodGet, "/api/v1/instances/{id}", s.handleGetInstance, false)
	api(http.MethodPatch, "/api/v1/instances/{id}", s.handleUpdateInstance, false)
	api(http.MethodDelete, "/api/v1/instances/{id}", s.handleDeleteInstance, false)
	api(http.MethodGet, "/api/v1/instances/{id}/compose", s.handleComposePreview, false)
	api(http.MethodGet, "/api/v1/instances/{id}/logs", s.handleInstanceLogs, false)
	api(http.MethodGet, "/api/v1/instances/{id}/backups", s.handleListBackups, false)
	api(http.MethodPost, "/api/v1/instances/{id}/backups", s.handleBackupUpload, false)
	api(http.MethodPost, "/api/v1/instances/{id}/actions/{action}", s.handleInstanceAction, false)

	// Backups.
	api(http.MethodGet, "/api/v1/backups/{id}/download", s.handleDownloadBackup, false)
	api(http.MethodDelete, "/api/v1/backups/{id}", s.handleDeleteBackup, false)

	// Jobs.
	api(http.MethodGet, "/api/v1/jobs", s.handleListJobs, false)
	api(http.MethodGet, "/api/v1/jobs/{id}", s.handleGetJob, false)
	api(http.MethodGet, "/api/v1/jobs/{id}/events", s.handleJobEvents, false)
	api(http.MethodPost, "/api/v1/jobs/{id}/cancel", s.handleCancelJob, false)
	api(http.MethodPost, "/api/v1/jobs/{id}/retry", s.handleRetryJob, false)

	// Settings and audit.
	api(http.MethodGet, "/api/v1/settings", s.handleSettings, false)
	api(http.MethodPatch, "/api/v1/settings", s.handleUpdateSettings, false)
	api(http.MethodGet, "/api/v1/audit-logs", s.handleAuditLogs, false)

	// Unknown API paths must not fall through to the SPA.
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, http.StatusNotFound, CodeNotFound, "unknown API endpoint")
	}))

	if webFS != nil {
		mux.Handle("/", spaHandler(webFS))
	}

	return withMiddleware(mux, corsAllowed)
}

func withMiddleware(next http.Handler, corsAllowed string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; connect-src 'self'; font-src 'self' data:; frame-ancestors 'none'")
		if corsAllowed != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if origin != "" && origin == corsAllowed {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
				h.Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				h.Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// spaHandler serves embedded assets and falls back to index.html for client
// routes so a deep link keeps working after a refresh.
func spaHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))
	index, indexErr := fs.ReadFile(webFS, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || p == "." {
			p = "index.html"
		}
		if st, err := fs.Stat(webFS, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if indexErr != nil {
			http.Error(w, "web UI is not built; run `make web`", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

// statusWriter captures the response status for request logging.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker so WebSocket upgrades (gorilla/websocket)
// work even when this wrapper is in the middleware chain.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

// RequestLogger logs one line per request without bodies, and skips noisy SSE
// heartbeats.
func RequestLogger(log interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			if sw.status >= 500 {
				log.Warn("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(start).String())
				return
			}
			log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(start).String())
		})
	}
}
