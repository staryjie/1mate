package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"1mate/internal/audit"
	"1mate/internal/auth"
	"1mate/internal/compose"
	"1mate/internal/job"
	"1mate/internal/model"
	"1mate/internal/service"
	"1mate/internal/store"
)

// Server holds everything the HTTP handlers need.
type Server struct {
	log     *slog.Logger
	store   *store.Store
	auth    *auth.Manager
	svc     *service.Service
	jobs    *job.Manager
	audit   *audit.Recorder
	version string
}

func NewServer(log *slog.Logger, st *store.Store, am *auth.Manager, svc *service.Service, jobs *job.Manager, rec *audit.Recorder, version string) *Server {
	return &Server{log: log, store: st, auth: am, svc: svc, jobs: jobs, audit: rec, version: version}
}

type ctxKey string

const (
	ctxSession ctxKey = "session"
	ctxUser    ctxKey = "user"
)

// requireAuth guards API routes and enforces CSRF on mutations.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.auth.Authenticate(r)
		if err != nil {
			s.fail(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			header := r.Header.Get("X-CSRF-Token")
			if header == "" || header != sess.CSRFToken {
				s.fail(w, http.StatusForbidden, CodeForbidden, "invalid CSRF token")
				return
			}
		}
		user, err := s.store.GetUserByID(sess.UserID)
		if err != nil {
			s.fail(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
			return
		}
		ctx := context.WithValue(r.Context(), ctxSession, sess)
		ctx = context.WithValue(ctx, ctxUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sessionFrom(ctx context.Context) *model.Session {
	v, _ := ctx.Value(ctxSession).(*model.Session)
	return v
}

func userFrom(ctx context.Context) *model.User {
	v, _ := ctx.Value(ctxUser).(*model.User)
	return v
}

func (s *Server) actor(r *http.Request) string {
	if u := userFrom(r.Context()); u != nil {
		return u.Username
	}
	return "anonymous"
}

// ---- auth handlers ----

type setupReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_required": s.auth.SetupRequired(),
		"version":        s.version,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupReq
	if !s.decode(w, r, &req) {
		return
	}
	if !s.auth.SetupRequired() {
		s.fail(w, http.StatusConflict, CodeConflict, "the platform is already set up")
		return
	}
	u, err := s.auth.CreateFirstUser(req.Username, req.Password)
	if err != nil {
		s.failFrom(w, wrapValidation(err))
		return
	}
	s.audit.Log(u.Username, "user.setup", "user", u.ID, nil, r)
	sess, token, err := s.auth.Login(r.Context(), u.Username, req.Password, audit.ClientIP(r), r.UserAgent())
	if err != nil {
		s.failFrom(w, err)
		return
	}
	s.auth.SetCookie(w, token)
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":       u,
		"csrf_token": sess.CSRFToken,
	})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !s.decode(w, r, &req) {
		return
	}
	sess, token, err := s.auth.Login(r.Context(), req.Username, req.Password, audit.ClientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, auth.ErrUnauthorized) {
			s.audit.Log(req.Username, "user.login_failed", "user", "", nil, r)
			s.fail(w, http.StatusUnauthorized, CodeUnauthorized, "invalid username or password")
			return
		}
		s.fail(w, http.StatusTooManyRequests, "rate_limited", err.Error())
		return
	}
	u, _ := s.store.GetUserByID(sess.UserID)
	actor := req.Username
	if u != nil {
		actor = u.Username
	}
	s.audit.Log(actor, "user.login", "user", sess.UserID, nil, r)
	s.auth.SetCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       u,
		"csrf_token": sess.CSRFToken,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess := sessionFrom(r.Context()); sess != nil {
		s.auth.Logout(sess)
	}
	s.auth.ClearCookie(w)
	s.audit.Log(s.actor(r), "user.logout", "user", "", nil, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"user":         userFrom(r.Context()),
		"csrf_token":   sess.CSRFToken,
		"setup_required": false,
		"version":      s.version,
	})
}

type changePasswordReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordReq
	if !s.decode(w, r, &req) {
		return
	}
	u := userFrom(r.Context())
	if u == nil {
		s.fail(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
		return
	}
	if err := s.auth.ChangePassword(u.ID, req.OldPassword, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrUnauthorized) {
			s.fail(w, http.StatusUnauthorized, CodeUnauthorized, "current password is incorrect")
			return
		}
		s.failFrom(w, wrapValidation(err))
		return
	}
	s.audit.Log(s.actor(r), "user.change_password", "user", u.ID, nil, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- hosts ----

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts()
	if err != nil {
		s.failFrom(w, err)
		return
	}
	type hostView struct {
		model.Host
		Capabilities *model.Capabilities `json:"capabilities,omitempty"`
		Instances    int                 `json:"instance_count"`
	}
	out := make([]hostView, 0, len(hosts))
	for i := range hosts {
		v := hostView{Host: hosts[i]}
		if caps, err := s.store.GetCapabilities(hosts[i].ID); err == nil {
			v.Capabilities = caps
		}
		if n, err := s.store.CountInstancesForHost(hosts[i].ID); err == nil {
			v.Instances = n
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	var in service.HostInput
	if !s.decode(w, r, &in) {
		return
	}
	h, err := s.svc.CreateHost(in)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "host.create", "host", h.ID, map[string]any{
		"name": h.Name, "address": h.Address, "ssh_user": h.SSHUser, "auth_type": h.AuthType,
		"connection_type": h.ConnectionType,
	}, r)
	// Agent 令牌只在创建时返回一次，前端需要它来配置 Agent 客户端。
	resp := map[string]any{"host": h}
	if h.ConnectionType == model.ConnTypeAgent {
		resp["agent_token"] = h.AgentToken
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h, err := s.store.GetHost(id)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	resp := map[string]any{"host": h}
	if caps, err := s.store.GetCapabilities(id); err == nil {
		resp["capabilities"] = caps
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	var in service.HostInput
	if !s.decode(w, r, &in) {
		return
	}
	h, err := s.svc.UpdateHost(r.PathValue("id"), in)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "host.update", "host", h.ID, map[string]any{"name": h.Name}, r)
	resp := map[string]any{"host": h}
	if h.ConnectionType == model.ConnTypeAgent && in.AgentToken != "" {
		// 只有显式轮换令牌时才返回新令牌。
		resp["agent_token"] = h.AgentToken
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.DeleteHost(id); err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "host.delete", "host", id, nil, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleProbeHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.jobs.Submit(model.JobHostProbe, id, "", "{}", s.actor(r))
	if err != nil {
		s.mapJobErr(w, err)
		return
	}
	s.audit.Log(s.actor(r), "host.probe", "host", id, map[string]any{"job_id": j.ID}, r)
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": j.ID})
}

// handleTestConnection performs an inline SSH check before a host is stored.
func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	var in service.TestConnectionInput
	if !s.decode(w, r, &in) {
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	res, err := s.svc.TestConnection(ctx, in)
	if err != nil && res == nil {
		s.failFrom(w, err)
		return
	}
	if res != nil && res.Fingerprint != "" {
		s.audit.Log(s.actor(r), "host.test_connection", "host", "", map[string]any{
			"address": in.Address, "ok": res.OK, "fingerprint": res.Fingerprint,
		}, r)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	caps, err := s.svc.CapabilitiesFor(r.PathValue("id"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, caps)
}

// ---- 主机终端 (WebSocket) ----

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4 * 1024,
	WriteBufferSize: 4 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow WebSocket connections from any origin for terminal access.
		// The session cookie is still required for authentication.
		return true
	},
}

// terminalMsg is the client→server message framing over the terminal WebSocket.
type terminalMsg struct {
	Type  string `json:"type"` // "input" | "resize"
	Data  string `json:"data"`  // base64-encoded input bytes (for "input")
	Rows  int    `json:"rows"`  // for "resize"
	Cols  int    `json:"cols"`  // for "resize"
}

// terminalOutput is the server→client message framing.
type terminalOutput struct {
	Type  string `json:"type"` // "output" | "exit"
	Data  string `json:"data"`  // base64-encoded output bytes (for "output")
	Code  int    `json:"code"`  // exit code (for "exit")
}

// handleHostTerminal upgrades the connection to a WebSocket and bridges it to
// an interactive SSH PTY session on the target host.
func (s *Server) handleHostTerminal(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")

	// Authenticate via session cookie (browser sends cookies with WS handshake).
	sess, err := s.auth.Authenticate(r)
	if err != nil {
		s.log.Error("websocket auth failed", "hostID", hostID, "error", err)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	_ = sess
	s.log.Debug("websocket auth ok", "hostID", hostID)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error("websocket upgrade failed", "hostID", hostID, "error", err)
		return
	}
	s.log.Debug("websocket upgraded", "hostID", hostID)
	defer conn.Close()

	// Create the PTY session on the remote host.
	ptyID, err := s.svc.CreatePtySession(hostID, "xterm-256color", 24, 80)
	if err != nil {
		s.log.Error("pty session failed", "hostID", hostID, "error", err)
		// WebSocket already upgraded; cannot send HTTP error. Notify client via WS frame.
		_ = conn.WriteJSON(terminalOutput{Type: "exit", Code: 1, Data: base64.StdEncoding.EncodeToString([]byte("terminal error: "+err.Error()+"\r\n"))})
		_ = s.svc.PtyClose(ptyID)
		return
	}
	defer s.svc.PtyClose(ptyID)

	// Notify client that the terminal is ready.
	_ = conn.WriteJSON(terminalOutput{Type: "output", Data: base64.StdEncoding.EncodeToString([]byte("\r\n[1mate terminal ready]\r\n"))})

	// Forward PTY output → WebSocket.
	outputDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 4*1024)
		for {
			n, err := s.svc.PtyRead(ptyID, buf)
			if n > 0 {
				_ = conn.WriteJSON(terminalOutput{
					Type: "output",
					Data: base64.StdEncoding.EncodeToString(buf[:n]),
				})
			}
			if err != nil {
				outputDone <- err
				return
			}
		}
	}()

	// WebSocket → PTY input.
	for {
		var msg terminalMsg
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseAbnormalClosure,
				websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
				s.log.Error("websocket read error", "error", err)
			}
			return
		}
		switch msg.Type {
		case "input":
			data, decodeErr := base64.StdEncoding.DecodeString(msg.Data)
			if decodeErr != nil {
				continue
			}
			if writeErr := s.svc.PtyWrite(ptyID, data); writeErr != nil {
				s.log.Error("pty write error", "error", writeErr)
				return
			}
		case "resize":
			if msg.Rows > 0 && msg.Cols > 0 {
				_ = s.svc.PtyResize(ptyID, msg.Rows, msg.Cols)
			}
		}
	}
}

// ---- Agent 连接 (WebSocket) ----

// handleAgentConnect upgrades the connection to a WebSocket and registers the
// Agent client with the AgentHub.
//
// Agent 是独立进程，没有浏览器登录会话，因此这里不使用 requireAuth，而是用
// agent_token 做机器认证：token 是高熵随机值，与主机一一绑定，足以证明身份。
// 客户端只需配置 AGENT_TOKEN 即可接入，无需关心 host_id / agent_id。
func (s *Server) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	// 优先从请求头读取（G7）：URL 会被反向代理、systemd 日志、浏览器历史
	// 记录，令牌不应出现在 query string 里。query 参数保留兼容旧客户端。
	agentToken := r.Header.Get("X-Agent-Token")
	if agentToken == "" {
		agentToken = r.Header.Get("Authorization")
		agentToken = strings.TrimPrefix(agentToken, "Bearer ")
	}
	if agentToken == "" {
		agentToken = r.URL.Query().Get("agent_token")
	}
	if agentToken == "" {
		s.log.Error("agent websocket missing token")
		http.Error(w, "agent token is required", http.StatusBadRequest)
		return
	}

	// 通过 token 反查主机，同时完成认证与主机定位。
	h, err := s.store.FindHostByAgentToken(agentToken)
	if err != nil {
		s.log.Error("agent token not recognized", "error", err)
		http.Error(w, "invalid agent_token", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error("agent websocket upgrade failed", "agent_id", h.AgentID, "error", err)
		return
	}

	// 用主机记录里的 AgentID 注册，命令路由（agentx.New(h.AgentID)）依赖该值。
	s.log.Info("agent connected", "agent_id", h.AgentID, "host_id", h.ID)
	s.svc.RegisterAgent(h.AgentID, h.ID, conn)
	// RegisterAgent blocks until the connection closes; no return needed here.
}

type imageSyncReq struct {
	SourceHostID string   `json:"source_host_id"`
	Images       []string `json:"images"`
}

// handleImagePrecheck 检查主机缺失哪些镜像（只读）。
func (s *Server) handleImagePrecheck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Images []string `json:"images"`
	}
	// body 可选：不带 body 时检查主机上所有实例需要的镜像。
	_ = s.decode(w, r, &req)
	results, err := s.svc.ImagePrecheck(id, req.Images)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// handleImageSync 发起主机间镜像同步任务。
func (s *Server) handleImageSync(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("id")
	var req imageSyncReq
	if !s.decode(w, r, &req) {
		return
	}
	if req.SourceHostID == "" {
		s.fail(w, http.StatusBadRequest, CodeValidation, "source_host_id is required")
		return
	}
	if req.SourceHostID == targetID {
		s.fail(w, http.StatusBadRequest, CodeValidation, "source and target host must differ")
		return
	}
	if len(req.Images) == 0 {
		s.fail(w, http.StatusBadRequest, CodeValidation, "images is required")
		return
	}
	payload := service.MarshalPayload(service.ImageSyncPayload{
		SourceHostID: req.SourceHostID,
		TargetHostID: targetID,
		Images:       req.Images,
	})
	j, err := s.jobs.Submit(model.JobImageSync, targetID, "", payload, s.actor(r))
	if err != nil {
		s.mapJobErr(w, err)
		return
	}
	s.audit.Log(s.actor(r), "image.sync", "host", targetID, map[string]any{
		"job_id": j.ID, "source_host_id": req.SourceHostID, "images": len(req.Images),
	}, r)
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": j.ID})
}

// ---- provisioning ----

type installReq struct {
	Strategy            string   `json:"strategy"`
	DockerVersion       string   `json:"docker_version"`
	Mirror              string   `json:"mirror"`
	ComposeVersion      string   `json:"compose_version"`
	ComposeURLBase      string   `json:"compose_url_base"`
	RegistryMirrors     []string `json:"registry_mirrors"`
	SaveSudoPassword    string   `json:"save_sudo_password"`
	AcceptSystemChanges bool     `json:"accept_system_changes"`
}

func (s *Server) installPayload(id string, req installReq) service.InstallPayload {
	return service.InstallPayload{
		HostID:              id,
		Strategy:            req.Strategy,
		DockerVersion:       req.DockerVersion,
		Mirror:              req.Mirror,
		ComposeVersion:      req.ComposeVersion,
		ComposeURLBase:      req.ComposeURLBase,
		RegistryMirrors:     req.RegistryMirrors,
		SaveSudoPassword:    req.SaveSudoPassword,
		AcceptSystemChanges: req.AcceptSystemChanges,
	}
}

// handleAgentInstallCommand 生成 Agent 模式主机的一键安装命令。
// server_base_url 可选；缺省时根据请求 Host 头推导。
func (s *Server) handleAgentInstallCommand(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	base := r.URL.Query().Get("server_base_url")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	script, err := s.svc.AgentInstallCommand(id, base)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "host.agent_install_command", "host", id, nil, r)
	writeJSON(w, http.StatusOK, map[string]any{"script": script, "server_base_url": base})
}

func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	req := installReq{
		Strategy:       r.URL.Query().Get("strategy"),
		DockerVersion:  r.URL.Query().Get("docker_version"),
		Mirror:         r.URL.Query().Get("mirror"),
		ComposeVersion: r.URL.Query().Get("compose_version"),
	}
	body, opts, err := s.svc.InstallScript(id, s.installPayload(id, req))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"script": body,
		"options": map[string]any{
			"strategy":        opts.Strategy,
			"mirror":          opts.Mirror,
			"docker_version":  opts.DockerVersion,
			"compose_version": opts.ComposeVersion,
		},
	})
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req installReq
	if !s.decode(w, r, &req) {
		return
	}
	if !req.AcceptSystemChanges {
		s.fail(w, http.StatusBadRequest, CodeValidation,
			"accept_system_changes must be true: installing Docker modifies the remote host")
		return
	}
	p := s.installPayload(id, req)
	// Validate the options before queueing so the user gets an immediate error.
	if _, err := s.svc.InstallOptionsFor(id, p); err != nil {
		s.failFrom(w, wrapValidation(err))
		return
	}
	j, err := s.jobs.Submit(model.JobInstallDocker, id, "", service.MarshalPayload(p), s.actor(r))
	if err != nil {
		s.mapJobErr(w, err)
		return
	}
	s.audit.Log(s.actor(r), "host.install_docker", "host", id, map[string]any{
		"job_id": j.ID, "strategy": p.Strategy, "docker_version": p.DockerVersion,
		"mirror": p.Mirror, "compose_version": p.ComposeVersion,
		"registry_mirrors": p.RegistryMirrors, "saved_sudo_password": p.SaveSudoPassword != "",
	}, r)
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": j.ID})
}

// ---- instances ----

func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host_id")
	list, err := s.store.ListInstances(hostID)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": list})
}

func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	var in service.InstanceInput
	if !s.decode(w, r, &in) {
		return
	}
	inst, err := s.svc.CreateInstance(in)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	public := publicInstance(inst)
	s.audit.Log(s.actor(r), "instance.create", "instance", inst.ID, map[string]any{
		"name": inst.Name, "slug": inst.Slug, "host_id": inst.HostID,
		"ports": []int{inst.TMPort, inst.GrafanaPort, inst.MQTTPort},
	}, r)
	writeJSON(w, http.StatusCreated, public)
}

func (s *Server) handleImportInstance(w http.ResponseWriter, r *http.Request) {
	var in service.ImportInstanceInput
	if !s.decode(w, r, &in) {
		return
	}
	inst, err := s.svc.ImportInstance(in)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	public := publicInstance(inst)
	s.audit.Log(s.actor(r), "instance.import", "instance", inst.ID, map[string]any{
		"name": inst.Name, "slug": inst.Slug, "host_id": inst.HostID,
		"base_dir": inst.BaseDir, "compose_major": inst.ComposeMajor,
	}, r)
	writeJSON(w, http.StatusCreated, public)
}

func (s *Server) handleGetInstance(w http.ResponseWriter, r *http.Request) {
	inst, err := s.store.GetInstance(r.PathValue("id"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicInstance(inst))
}

func (s *Server) handleUpdateInstance(w http.ResponseWriter, r *http.Request) {
	var in service.InstanceInput
	if !s.decode(w, r, &in) {
		return
	}
	inst, err := s.svc.UpdateInstance(r.PathValue("id"), in)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "instance.update", "instance", inst.ID, map[string]any{
		"name": inst.Name, "ports": []int{inst.TMPort, inst.GrafanaPort, inst.MQTTPort},
	}, r)
	writeJSON(w, http.StatusOK, publicInstance(inst))
}

func (s *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.svc.DeleteInstance(id); err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "instance.delete_record", "instance", id, nil, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleComposePreview(w http.ResponseWriter, r *http.Request) {
	inst, err := s.svc.GetInstance(r.PathValue("id"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	composeBody, envBody, err := s.svc.RenderPreview(inst)
	if err != nil {
		s.failFrom(w, wrapValidation(err))
		return
	}
	cmdBits, kind := compose.Command(model.ComposeKind(composeKindFromMajor(inst.ComposeMajor)))
	writeJSON(w, http.StatusOK, map[string]any{
		"compose":        composeBody,
		"env":            envBody,
		"compose_kind":   kind,
		"command_prefix": cmdBits,
		"project_name":   inst.ProjectName,
		"base_dir":       inst.BaseDir,
	})
}

func composeKindFromMajor(major int) model.ComposeKind {
	if major == 1 {
		return model.ComposeStandaloneV1
	}
	return model.ComposePluginV2
}

func (s *Server) handleRevealKey(w http.ResponseWriter, r *http.Request) {
	inst, err := s.svc.GetInstance(r.PathValue("id"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	s.audit.Log(s.actor(r), "instance.reveal_key", "instance", inst.ID, nil, r)
	writeJSON(w, http.StatusOK, map[string]any{
		"tm_encryption_key": inst.TMEncryptionKey,
		"notice":            "keep this key safe: restoring a backup into a new instance requires the same key",
	})
}

type actionReq struct {
	PurgeVolumes bool   `json:"purge_volumes"`
	ConfirmSlug  string `json:"confirm_slug"`
	BackupID     string `json:"backup_id"`
	Note         string `json:"note"`
	// PostgresTag is the target PostgreSQL major tag for upgrade actions.
	// If empty, the current instance tag is kept (no major version change).
	PostgresTag string `json:"postgres_tag"`
}

func (s *Server) handleInstanceAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.PathValue("action")
	inst, err := s.store.GetInstance(id)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	var req actionReq
	if action == "destroy" || action == "restore" || action == "upgrade" {
		if !s.decode(w, r, &req) {
			return
		}
	}

	var kind model.JobKind
	var payload string
	switch action {
	case "reveal-key":
		writeJSON(w, http.StatusOK, map[string]any{
			"tm_encryption_key": inst.TMEncryptionKey,
			"notice":            "keep this key safe: restoring a backup into a new instance requires the same key",
		})
		return
	case "deploy":
		kind, payload = model.JobDeploy, service.MarshalPayload(service.SimplePayload{InstanceID: id})
	case "start":
		kind, payload = model.JobStart, service.MarshalPayload(service.SimplePayload{InstanceID: id})
	case "stop":
		kind, payload = model.JobStop, service.MarshalPayload(service.SimplePayload{InstanceID: id})
	case "restart":
		kind, payload = model.JobRestart, service.MarshalPayload(service.SimplePayload{InstanceID: id})
	case "reconcile":
		kind, payload = model.JobReconcile, service.MarshalPayload(service.SimplePayload{InstanceID: id})
	case "upgrade":
		newTag := req.PostgresTag
		if newTag == "" {
			newTag = inst.PostgresTag
		}
		if err := service.UpgradeGuard(inst.PostgresTag, newTag); err != nil {
			s.failFrom(w, err)
			return
		}
		kind, payload = model.JobUpgrade, service.MarshalPayload(service.SimplePayload{InstanceID: id})
	case "backup":
		if req.Note == "" {
			req.Note = "manual backup"
		}
		kind, payload = model.JobBackup, service.MarshalPayload(service.BackupPayload{InstanceID: id, Note: req.Note})
	case "restore":
		if req.BackupID == "" {
			s.fail(w, http.StatusBadRequest, CodeValidation, "backup_id is required")
			return
		}
		kind, payload = model.JobRestore, service.MarshalPayload(service.RestorePayload{InstanceID: id, BackupID: req.BackupID})
	case "destroy":
		if req.PurgeVolumes && req.ConfirmSlug != inst.Slug {
			s.fail(w, http.StatusBadRequest, CodeValidation, "confirm_slug must match the instance slug to delete data")
			return
		}
		kind = model.JobDestroy
		payload = service.MarshalPayload(service.DestroyPayload{
			InstanceID: id, PurgeVolumes: req.PurgeVolumes, ConfirmSlug: req.ConfirmSlug,
		})
	default:
		s.fail(w, http.StatusNotFound, CodeNotFound, "unknown action")
		return
	}

	j, err := s.jobs.Submit(kind, inst.HostID, id, payload, s.actor(r))
	if err != nil {
		s.mapJobErr(w, err)
		return
	}
	s.audit.Log(s.actor(r), "instance."+action, "instance", id, map[string]any{
		"job_id": j.ID, "purge_volumes": req.PurgeVolumes, "backup_id": req.BackupID,
	}, r)
	if kind == model.JobDeploy {
		_ = s.store.SetInstanceStatus(id, model.InstDeploying, "queued", j.ID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": j.ID})
}

// ---- jobs ----

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	jobs, err := s.store.ListJobs(store.JobFilter{
		HostID:     q.Get("host_id"),
		InstanceID: q.Get("instance_id"),
		Limit:      50,
	})
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.store.GetJob(r.PathValue("id"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	logText, _ := job.ReadLogFile(j.LogPath)
	writeJSON(w, http.StatusOK, map[string]any{"job": j, "log": s.svc.Red.Apply(logText)})
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.jobs.Cancel(id); err != nil {
		s.fail(w, http.StatusConflict, CodeConflict, "the job is not running")
		return
	}
	s.audit.Log(s.actor(r), "job.cancel", "job", id, nil, r)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// handleRetryJob 用原任务的同种类型、目标与 payload 重新提交一个新任务。
// 只允许对已结束（成功/失败/取消/中断）的任务重试。
func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.store.GetJob(id)
	if err != nil {
		s.failFrom(w, err)
		return
	}
	if !j.Status.Terminal() {
		s.fail(w, http.StatusConflict, CodeConflict, "the job is still running")
		return
	}
	nj, err := s.jobs.Submit(j.Kind, j.HostID, j.InstanceID, j.Payload, s.actor(r))
	if err != nil {
		s.mapJobErr(w, err)
		return
	}
	s.audit.Log(s.actor(r), "job.retry", "job", id, map[string]any{"new_job_id": nj.ID}, r)
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": nj.ID})
}

// ---- misc ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version, "time": time.Now().UTC()})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.AllSettings()
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": all})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if !s.decode(w, r, &in) {
		return
	}
	allowed := map[string]bool{
		"require_compose_v2": true, "compose_binary_version": true,
		"default_pkg_mirror": true, "compose_download_base_url": true,
	}
	for k, v := range in {
		if !allowed[k] {
			s.fail(w, http.StatusBadRequest, CodeValidation, "unknown setting "+k)
			return
		}
		switch k {
		case "require_compose_v2":
			if v != "true" && v != "false" {
				s.fail(w, http.StatusBadRequest, CodeValidation, "require_compose_v2 must be true or false")
				return
			}
		case "compose_binary_version":
			if !versionOnly(v) {
				s.fail(w, http.StatusBadRequest, CodeValidation, "compose_binary_version must look like 2.29.7")
				return
			}
		case "default_pkg_mirror":
			if v != "" && v != "none" && v != "Aliyun" && v != "AzureChinaCloud" {
				s.fail(w, http.StatusBadRequest, CodeValidation, "default_pkg_mirror must be none, Aliyun or AzureChinaCloud")
				return
			}
		case "compose_download_base_url":
			if v != "" && !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
				s.fail(w, http.StatusBadRequest, CodeValidation, "compose_download_base_url must be an http(s) URL")
				return
			}
		}
		if err := s.store.SetSetting(k, v); err != nil {
			s.failFrom(w, err)
			return
		}
	}
	s.audit.Log(s.actor(r), "settings.update", "settings", "", in, r)
	all, _ := s.store.AllSettings()
	writeJSON(w, http.StatusOK, map[string]any{"settings": all})
}

func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	list, err := s.store.ListAudit(limit, r.URL.Query().Get("before"))
	if err != nil {
		s.failFrom(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": list})
}

func (s *Server) mapJobErr(w http.ResponseWriter, err error) {
	if errors.Is(err, job.ErrConflict) {
		s.fail(w, http.StatusConflict, CodeConflict, "another operation is already running for this target")
		return
	}
	s.failFrom(w, err)
}

// publicInstance strips secrets from an instance body.
func publicInstance(inst *model.Instance) map[string]any {
	cp := *inst
	cp.TMEncryptionKey = ""
	cp.DBPass = ""
	cp.GrafanaPass = ""
	b, _ := json.Marshal(cp)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	out["has_encryption_key"] = inst.TMEncryptionKey != ""
	return out
}

func versionOnly(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

func wrapValidation(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, service.ErrValidation) || errors.Is(err, service.ErrConflict) {
		return err
	}
	return &wrappedValidation{err: err}
}

type wrappedValidation struct{ err error }

func (w *wrappedValidation) Error() string { return w.err.Error() }
func (w *wrappedValidation) Unwrap() error { return service.ErrValidation }
