package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"1mate/internal/compose"
	"1mate/internal/cryptox"
	"1mate/internal/ids"
	"1mate/internal/model"
	"1mate/internal/sshx"
	"1mate/internal/store"
)

var (
	timezonePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-/]{1,63}$`)
	tagPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// InstanceInput is the create/update payload.
type InstanceInput struct {
	Name             string            `json:"name"`
	Slug             string            `json:"slug"`
	HostID           string            `json:"host_id"`
	BaseDir          string            `json:"base_dir"`
	BindAddress      string            `json:"bind_address"`
	TMPort           int               `json:"tm_port"`
	GrafanaPort      int               `json:"grafana_port"`
	MQTTPort         int               `json:"mqtt_port"`
	Timezone         string            `json:"timezone"`
	Region           string            `json:"region"`
	ImageTag         string            `json:"image_tag"`
	PostgresTag      string            `json:"postgres_tag"`
	GrafanaAdminUser string            `json:"grafana_admin_user"`
	DisableMQTT      bool              `json:"disable_mqtt"`
	ExtraEnv         map[string]string `json:"extra_env"`
}

func (in *InstanceInput) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.HostID = strings.TrimSpace(in.HostID)
	in.BaseDir = strings.TrimSpace(in.BaseDir)
	in.BindAddress = strings.TrimSpace(in.BindAddress)
	in.Timezone = strings.TrimSpace(in.Timezone)
	in.Region = strings.TrimSpace(in.Region)
	in.ImageTag = strings.TrimSpace(in.ImageTag)
	in.PostgresTag = strings.TrimSpace(in.PostgresTag)
	in.GrafanaAdminUser = strings.TrimSpace(in.GrafanaAdminUser)
	if in.BindAddress == "" {
		in.BindAddress = "0.0.0.0"
	}
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if in.Region == "" {
		in.Region = "global"
	}
	if in.ImageTag == "" {
		in.ImageTag = "latest"
	}
	if in.PostgresTag == "" {
		in.PostgresTag = "18-trixie"
	}
	if in.GrafanaAdminUser == "" {
		in.GrafanaAdminUser = "admin"
	}
	if in.Name == "" && in.Slug != "" {
		in.Name = in.Slug
	}
}

func (in InstanceInput) Validate() error {
	var problems []string
	if err := compose.ValidateSlug(in.Slug); err != nil {
		problems = append(problems, err.Error())
	}
	if len(in.Name) < 1 || len(in.Name) > 64 {
		problems = append(problems, "name must be 1-64 characters")
	}
	if in.HostID == "" {
		problems = append(problems, "host_id is required")
	}
	for _, p := range []struct {
		name string
		port int
	}{{"tm_port", in.TMPort}, {"grafana_port", in.GrafanaPort}, {"mqtt_port", in.MQTTPort}} {
		if p.port < 1 || p.port > 65535 {
			problems = append(problems, p.name+" must be between 1 and 65535")
		}
	}
	if in.TMPort == in.GrafanaPort || in.TMPort == in.MQTTPort || in.GrafanaPort == in.MQTTPort {
		problems = append(problems, "the three ports must be distinct")
	}
	if !timezonePattern.MatchString(in.Timezone) {
		problems = append(problems, "timezone must be an IANA name such as Asia/Shanghai")
	}
	switch in.Region {
	case "global", "cn":
	default:
		problems = append(problems, "region must be global or cn")
	}
	if !tagPattern.MatchString(in.ImageTag) {
		problems = append(problems, "image_tag is not a valid tag")
	}
	if !tagPattern.MatchString(in.PostgresTag) {
		problems = append(problems, "postgres_tag is not a valid tag")
	}
	if in.BaseDir != "" && !strings.HasPrefix(in.BaseDir, "/") {
		problems = append(problems, "base_dir must be an absolute path")
	}
	if strings.Contains(in.BaseDir, "..") {
		problems = append(problems, "base_dir must not contain ..")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(problems, "; "))
	}
	return nil
}

func newSecret(n int) (string, error) { return cryptox.RandomToken(n) }

// CreateInstance validates ports against both the platform and the remote host,
// stores secrets encrypted, and leaves the instance ready to deploy.
func (s *Service) CreateInstance(in InstanceInput) (*model.Instance, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return nil, err
	}
	host, err := s.Store.GetHost(in.HostID)
	if err != nil {
		return nil, fmt.Errorf("%w: host not found", ErrValidation)
	}
	caps, err := s.Store.GetCapabilities(in.HostID)
	if err != nil {
		return nil, fmt.Errorf("%w: probe host %q before creating instances", ErrValidation, host.Name)
	}
	if caps.UnsupportedReason != "" {
		return nil, fmt.Errorf("%w: %s", ErrValidation, caps.UnsupportedReason)
	}
	if _, ok := compose.Command(caps.ComposeKind); !ok {
		return nil, fmt.Errorf("%w: host %q has no usable Docker Compose; install it first", ErrValidation, host.Name)
	}

	holders, err := s.Store.PortHolders(in.HostID, "")
	if err != nil {
		return nil, err
	}
	for _, p := range []int{in.TMPort, in.GrafanaPort, in.MQTTPort} {
		if other, taken := holders[p]; taken {
			return nil, fmt.Errorf("%w: port %d is already used by another instance on this host (%s)", ErrValidation, p, other)
		}
	}
	for _, p := range []int{in.TMPort, in.GrafanaPort, in.MQTTPort} {
		if containsInt(caps.PortsInUse, p) {
			return nil, fmt.Errorf("%w: port %d is already listening on the host", ErrValidation, p)
		}
	}

	if _, err := s.Store.GetInstanceBySlug(in.HostID, in.Slug); err == nil {
		return nil, fmt.Errorf("%w: an instance with slug %q already exists on this host", ErrConflict, in.Slug)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	encKey, err := newSecret(24)
	if err != nil {
		return nil, err
	}
	dbPass, err := newSecret(24)
	if err != nil {
		return nil, err
	}
	grafanaPass, err := newSecret(18)
	if err != nil {
		return nil, err
	}

	baseDir := in.BaseDir
	if baseDir == "" {
		baseDir = compose.DefaultBaseDir(in.Slug)
	}
	inst := &model.Instance{
		ID:               ids.New(),
		Name:             in.Name,
		Slug:             in.Slug,
		HostID:           in.HostID,
		ProjectName:      compose.ProjectName(in.Slug),
		BaseDir:          baseDir,
		BindAddress:      in.BindAddress,
		TMPort:           in.TMPort,
		GrafanaPort:      in.GrafanaPort,
		MQTTPort:         in.MQTTPort,
		Timezone:         in.Timezone,
		Region:           in.Region,
		ImageTag:         in.ImageTag,
		PostgresTag:      in.PostgresTag,
		GrafanaAdminUser: in.GrafanaAdminUser,
		DisableMQTT:      in.DisableMQTT,
		ExtraEnv:         in.ExtraEnv,
		DBUser:           "teslamate",
		ComposeMajor:     caps.ComposeKind.Major(),
		Status:           model.InstCreated,
	}
	if inst.ExtraEnv == nil {
		inst.ExtraEnv = map[string]string{}
	}
	wrapped, err := s.wrapInstanceSecrets(inst.ID, encKey, dbPass, grafanaPass)
	if err != nil {
		return nil, err
	}
	inst.TMEncryptionKey, inst.DBPass, inst.GrafanaPass = wrapped.enc, wrapped.db, wrapped.grafana
	if err := s.Store.CreateInstance(inst); err != nil {
		return nil, err
	}
	return s.LoadInstanceSecrets(inst)
}

type wrappedSecrets struct{ enc, db, grafana string }

// wrapInstanceSecrets seals the three instance secrets with AAD bound to the
// instance id, so a copied value cannot be decrypted for another instance.
func (s *Service) wrapInstanceSecrets(instanceID, enc, db, grafana string) (wrappedSecrets, error) {
	var out wrappedSecrets
	var err error
	if out.enc, err = s.Box.Encrypt(enc, "instances:"+instanceID+":encryption_key"); err != nil {
		return out, err
	}
	if out.db, err = s.Box.Encrypt(db, "instances:"+instanceID+":db_pass"); err != nil {
		return out, err
	}
	if out.grafana, err = s.Box.Encrypt(grafana, "instances:"+instanceID+":grafana_pass"); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) unwrapInstanceSecrets(inst *model.Instance) error {
	var err error
	if inst.TMEncryptionKey, err = s.Box.Decrypt(inst.TMEncryptionKey, "instances:"+inst.ID+":encryption_key"); err != nil {
		return err
	}
	if inst.DBPass, err = s.Box.Decrypt(inst.DBPass, "instances:"+inst.ID+":db_pass"); err != nil {
		return err
	}
	if inst.GrafanaPass, err = s.Box.Decrypt(inst.GrafanaPass, "instances:"+inst.ID+":grafana_pass"); err != nil {
		return err
	}
	return nil
}

// LoadInstanceSecrets returns a copy with plaintext secrets for internal use.
func (s *Service) LoadInstanceSecrets(inst *model.Instance) (*model.Instance, error) {
	cp := *inst
	if err := s.unwrapInstanceSecrets(&cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// GetInstance loads an instance with secrets decrypted.
func (s *Service) GetInstance(id string) (*model.Instance, error) {
	inst, err := s.Store.GetInstance(id)
	if err != nil {
		return nil, err
	}
	return s.LoadInstanceSecrets(inst)
}

// UpdateInstance applies an editable configuration change. Ports and slug are
// checked again; existing data is untouched.
func (s *Service) UpdateInstance(id string, in InstanceInput) (*model.Instance, error) {
	inst, err := s.GetInstance(id)
	if err != nil {
		return nil, err
	}
	in.Normalize()
	if in.HostID == "" {
		in.HostID = inst.HostID
	}
	if in.HostID != inst.HostID {
		return nil, fmt.Errorf("%w: changing the host of an instance is not supported; create a new instance instead", ErrValidation)
	}
	if in.Slug == "" {
		in.Slug = inst.Slug
	}
	if in.Slug != inst.Slug {
		return nil, fmt.Errorf("%w: the slug cannot be changed after creation", ErrValidation)
	}
	if in.Name == "" {
		in.Name = inst.Name
	}
	if in.TMPort == 0 {
		in.TMPort = inst.TMPort
	}
	if in.GrafanaPort == 0 {
		in.GrafanaPort = inst.GrafanaPort
	}
	if in.MQTTPort == 0 {
		in.MQTTPort = inst.MQTTPort
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}

	holders, err := s.Store.PortHolders(inst.HostID, inst.ID)
	if err != nil {
		return nil, err
	}
	for _, p := range []int{in.TMPort, in.GrafanaPort, in.MQTTPort} {
		if other, taken := holders[p]; taken {
			return nil, fmt.Errorf("%w: port %d is already used by another instance on this host (%s)", ErrValidation, p, other)
		}
	}
	portsChanged := in.TMPort != inst.TMPort || in.GrafanaPort != inst.GrafanaPort || in.MQTTPort != inst.MQTTPort
	if portsChanged {
		if caps, err := s.Store.GetCapabilities(inst.HostID); err == nil {
			for _, p := range []int{in.TMPort, in.GrafanaPort, in.MQTTPort} {
				if containsInt(caps.PortsInUse, p) && !containsInt([]int{inst.TMPort, inst.GrafanaPort, inst.MQTTPort}, p) {
					return nil, fmt.Errorf("%w: port %d is already listening on the host", ErrValidation, p)
				}
			}
		}
	}

	baseDir := in.BaseDir
	if baseDir == "" {
		baseDir = inst.BaseDir
	}
	cp := *inst
	cp.Name = in.Name
	cp.BaseDir = baseDir
	cp.BindAddress = in.BindAddress
	cp.TMPort = in.TMPort
	cp.GrafanaPort = in.GrafanaPort
	cp.MQTTPort = in.MQTTPort
	cp.Timezone = in.Timezone
	cp.Region = in.Region
	cp.ImageTag = in.ImageTag
	cp.PostgresTag = in.PostgresTag
	cp.GrafanaAdminUser = in.GrafanaAdminUser
	cp.DisableMQTT = in.DisableMQTT
	if in.ExtraEnv != nil {
		cp.ExtraEnv = in.ExtraEnv
	}
	if err := s.Store.UpdateInstance(&cp); err != nil {
		return nil, err
	}
	return s.LoadInstanceSecrets(&cp)
}

// DeleteInstance removes an instance record. Remote data is only removed by the
// destroy job, so this is safe to call after a failed deploy.
func (s *Service) DeleteInstance(id string) error {
	inst, err := s.Store.GetInstance(id)
	if err != nil {
		return err
	}
	if active, err := s.Store.ActiveJobForInstance(inst.ID); err == nil && active != nil {
		return fmt.Errorf("%w: an operation is in progress for this instance", ErrConflict)
	}
	backups, err := s.Store.ListBackups(id)
	if err != nil {
		return err
	}
	for _, b := range backups {
		s.RemoveBackupFile(&b)
		_ = s.Store.DeleteBackup(b.ID)
	}
	return s.Store.DeleteInstance(id)
}

// specFor builds the compose spec from a decrypted instance.
func (s *Service) specFor(inst *model.Instance) compose.Spec {
	return compose.Spec{
		Slug:             inst.Slug,
		ProjectName:      inst.ProjectName,
		BindAddress:      inst.BindAddress,
		TMPort:           inst.TMPort,
		GrafanaPort:      inst.GrafanaPort,
		MQTTPort:         inst.MQTTPort,
		Timezone:         inst.Timezone,
		Region:           inst.Region,
		ImageTag:         inst.ImageTag,
		PostgresTag:      inst.PostgresTag,
		GrafanaAdminUser: inst.GrafanaAdminUser,
		DisableMQTT:      inst.DisableMQTT,
		ExtraEnv:         inst.ExtraEnv,
		EncryptionKey:    inst.TMEncryptionKey,
		DBUser:           inst.DBUser,
		DBPass:           inst.DBPass,
		GrafanaPassword:  inst.GrafanaPass,
	}
}

// RenderPreview returns the compose and env files with secrets masked.
func (s *Service) RenderPreview(inst *model.Instance) (composeBody, envBody string, err error) {
	spec := s.specFor(inst)
	major := inst.ComposeMajor
	if major == 0 {
		major = 2
	}
	composeBody, err = compose.Render(spec, major)
	if err != nil {
		return "", "", err
	}
	envBody, err = compose.RenderEnv(spec)
	if err != nil {
		return "", "", err
	}
	envBody = compose.HideSecrets(envBody, spec.EncryptionKey, spec.DBPass, spec.GrafanaPassword)
	envBody = s.Red.Apply(envBody)
	return composeBody, envBody, nil
}

// composeRunner bundles the remote compose invocation details for one host.
type composeRunner struct {
	client  sshx.Client
	sudo    sshx.Sudo
	prefix  string
	major   int
	baseDir string
	project string
	envFile string
	argv    string
}

func (s *Service) newRunner(ctx context.Context, inst *model.Instance) (*composeRunner, error) {
	host, err := s.Store.GetHost(inst.HostID)
	if err != nil {
		return nil, err
	}
	client, sudo, err := s.Connect(host)
	if err != nil {
		return nil, classifySSHError(err)
	}
	caps, err := s.Store.GetCapabilities(inst.HostID)
	if err != nil {
		s.closeQuietly(client)
		return nil, fmt.Errorf("%w: probe the host before running operations", ErrValidation)
	}
	cmdBits, ok := compose.Command(caps.ComposeKind)
	if !ok {
		s.closeQuietly(client)
		return nil, fmt.Errorf("%w: no usable Docker Compose on host %q", ErrValidation, host.Name)
	}
	major := caps.ComposeKind.Major()
	if inst.ComposeMajor != 0 && inst.ComposeMajor != major {
		// Respect the major version the instance was created with; if the host
		// lost that form, fail loudly instead of silently switching syntax.
		if inst.ComposeMajor == 1 && caps.ComposeKind != model.ComposeStandaloneV1 {
			s.closeQuietly(client)
			return nil, fmt.Errorf("%w: this instance was deployed with Compose v1 but the host now has %s; redeploy to migrate", ErrValidation, caps.ComposeKind)
		}
	}
	return &composeRunner{
		client:  client,
		sudo:    sudo,
		prefix:  dockerPrefix(sudo, caps),
		major:   major,
		baseDir: inst.BaseDir,
		project: inst.ProjectName,
		envFile: inst.BaseDir + "/.env",
		argv:    cmdBits,
	}, nil
}

func (r *composeRunner) Close() { r.client.Close() }

// cmd builds "cd <dir> && <compose> --env-file <env> -f compose.yaml -p <project> <args>"
func (r *composeRunner) cmd(args string) string {
	return fmt.Sprintf("cd %s && %s%s --env-file %s -f %s -p %s %s",
		sshx.Quote(r.baseDir), r.prefix, r.argv, sshx.Quote(r.envFile),
		sshx.Quote(r.baseDir+"/compose.yaml"), sshx.Quote(r.project), args)
}

func (r *composeRunner) run(ctx context.Context, args string, timeout time.Duration, sink func(string)) (sshx.Result, error) {
	opts := sshx.RunOptions{Timeout: timeout, Sudo: r.sudo}
	if sink == nil {
		return r.client.Run(ctx, r.cmd(args), opts)
	}
	return r.client.RunStreaming(ctx, r.cmd(args), opts, sink)
}

// RunCompose executes an arbitrary compose action for the instance.
func (s *Service) RunCompose(ctx context.Context, inst *model.Instance, args string, timeout time.Duration, sink func(string)) (sshx.Result, error) {
	r, err := s.newRunner(ctx, inst)
	if err != nil {
		return sshx.Result{}, err
	}
	defer r.Close()
	return r.run(ctx, args, timeout, sink)
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// SuggestPorts returns the first free port triple starting at a base.
func (s *Service) SuggestPorts(hostID string) (tm, grafana, mqtt int) {
	holders, _ := s.Store.PortHolders(hostID, "")
	var used []int
	if caps, err := s.Store.GetCapabilities(hostID); err == nil {
		used = caps.PortsInUse
	}
	base := 4000
	for i := 0; i < 200; i++ {
		t, g, m := base+i, 3000+i, 1883+i
		if _, ok := holders[t]; ok {
			continue
		}
		if _, ok := holders[g]; ok {
			continue
		}
		if _, ok := holders[m]; ok {
			continue
		}
		if containsInt(used, t) || containsInt(used, g) || containsInt(used, m) {
			continue
		}
		return t, g, m
	}
	return 4000, 3000, 1883
}

// ParsePort is used by query parameter parsing in the API.
func ParsePort(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}
