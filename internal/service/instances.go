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

// ImportInstanceInput is the payload for importing an already-deployed
// TeslaMate instance on a remote host.
type ImportInstanceInput struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	HostID      string `json:"host_id"`
	BaseDir     string `json:"base_dir"`
	ComposeMajor int   `json:"compose_major"`
	TMPort     int    `json:"tm_port"`
	GrafanaPort int    `json:"grafana_port"`
	MQTTPort    int    `json:"mqtt_port"`
	Region      string `json:"region"`
}

func (in *ImportInstanceInput) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.HostID = strings.TrimSpace(in.HostID)
	in.BaseDir = strings.TrimSpace(in.BaseDir)
}

func (in ImportInstanceInput) Validate() error {
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
	if in.BaseDir == "" {
		problems = append(problems, "base_dir is required")
	}
	if !strings.HasPrefix(in.BaseDir, "/") {
		problems = append(problems, "base_dir must be an absolute path")
	}
	if strings.Contains(in.BaseDir, "..") {
		problems = append(problems, "base_dir must not contain ..")
	}
	for _, p := range []struct {
		name string
		port int
	}{{"tm_port", in.TMPort}, {"grafana_port", in.GrafanaPort}, {"mqtt_port", in.MQTTPort}} {
		if p.port != 0 && (p.port < 1 || p.port > 65535) {
			problems = append(problems, p.name+" must be between 1 and 65535")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrValidation, strings.Join(problems, "; "))
	}
	return nil
}

// ImportInstance registers an already-deployed TeslaMate instance so 1mate can
// manage it. It reads compose.yaml and .env from the remote host, reuses the
// existing secrets instead of generating new ones, and does not overwrite any
// remote files.
func (s *Service) ImportInstance(in ImportInstanceInput) (*model.Instance, error) {
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
		return nil, fmt.Errorf("%w: probe host %q before importing instances", ErrValidation, host.Name)
	}
	if caps.UnsupportedReason != "" {
		return nil, fmt.Errorf("%w: %s", ErrValidation, caps.UnsupportedReason)
	}

	// Determine compose major version.
	major := in.ComposeMajor
	if major == 0 {
		major = caps.ComposeKind.Major()
	}
	if major != 1 && major != 2 {
		return nil, fmt.Errorf("%w: compose_major must be 1 or 2", ErrValidation)
	}

	client, _, err := s.Connect(host)
	if err != nil {
		return nil, classifySSHError(err)
	}
	defer client.Close()

	// Read .env from the remote host.
	envPath := in.BaseDir + "/.env"
	var envBuf strings.Builder
	if _, err := client.Download(context.Background(), envPath, &envBuf); err != nil {
		return nil, fmt.Errorf("%w: cannot read %s: %v", ErrValidation, envPath, err)
	}
	envVars := parseEnvFile(envBuf.String())

	// Read compose file (prefer compose.yaml, fall back to compose.yml,
	// then docker-compose.yml). Docker Compose v2 accepts all three names.
	composePath := in.BaseDir + "/compose.yaml"
	var composeBuf strings.Builder
	if _, err := client.Download(context.Background(), composePath, &composeBuf); err != nil {
		composePath = in.BaseDir + "/compose.yml"
		composeBuf.Reset()
		if _, err := client.Download(context.Background(), composePath, &composeBuf); err != nil {
			composePath = in.BaseDir + "/docker-compose.yml"
			composeBuf.Reset()
			if _, err := client.Download(context.Background(), composePath, &composeBuf); err != nil {
				return nil, fmt.Errorf("%w: cannot read compose file in %s: %v", ErrValidation, in.BaseDir, err)
			}
		}
	}
	_ = composeBuf // compose file is kept as-is; 1mate does not rewrite it on import.

	// Extract required secrets from .env.
	// Supports both 1mate-generated names (TM_ENCRYPTION_KEY, TM_DB_PASS,
	// GRAFANA_PW) and plain TeslaMate names (ENCRYPTION_KEY, DATABASE_PASS)
	// for instances deployed outside of 1mate.
	encKey := envVars["TM_ENCRYPTION_KEY"]
	if encKey == "" {
		encKey = envVars["ENCRYPTION_KEY"]
	}
	dbPass := envVars["TM_DB_PASS"]
	if dbPass == "" {
		dbPass = envVars["DATABASE_PASS"]
	}
	grafanaPass := envVars["GRAFANA_PW"]
	// Grafana password is optional: instances deployed outside of 1mate may
	// not pre-set it (Grafana defaults to admin/admin and prompts on first login).
	if grafanaPass == "" {
		grafanaPass = "admin"
	}
	if encKey == "" || dbPass == "" {
		return nil, fmt.Errorf("%w: .env is missing required secrets (ENCRYPTION_KEY/TM_ENCRYPTION_KEY and DATABASE_PASS/TM_DB_PASS)", ErrValidation)
	}

	// Extract ports from .env unless explicitly provided.
	tmPort := in.TMPort
	if tmPort == 0 {
		if v, ok := envVars["TM_TM_PORT"]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				tmPort = n
			}
		}
	}
	grafanaPort := in.GrafanaPort
	if grafanaPort == 0 {
		if v, ok := envVars["TM_GRAFANA_PORT"]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				grafanaPort = n
			}
		}
	}
	mqttPort := in.MQTTPort
	if mqttPort == 0 {
		if v, ok := envVars["TM_MQTT_PORT"]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				mqttPort = n
			}
		}
	}
	if tmPort < 1 || tmPort > 65535 || grafanaPort < 1 || grafanaPort > 65535 || mqttPort < 1 || mqttPort > 65535 {
		return nil, fmt.Errorf("%w: invalid ports parsed from .env", ErrValidation)
	}

	// Check port conflicts against other instances on this host.
	holders, err := s.Store.PortHolders(in.HostID, "")
	if err != nil {
		return nil, err
	}
	for _, p := range []int{tmPort, grafanaPort, mqttPort} {
		if other, taken := holders[p]; taken {
			return nil, fmt.Errorf("%w: port %d is already used by another instance on this host (%s)", ErrValidation, p, other)
		}
	}

	// Note: caps.PortsInUse is intentionally NOT checked for import — the
	// imported instance is already running and its ports are expected to be
	// in use on the host.

	// Check slug uniqueness.
	if _, err := s.Store.GetInstanceBySlug(in.HostID, in.Slug); err == nil {
		return nil, fmt.Errorf("%w: an instance with slug %q already exists on this host", ErrConflict, in.Slug)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	// Extract other settings from .env with defaults.
	imageTag := envVars["TM_IMAGE_TAG"]
	if imageTag == "" {
		imageTag = "latest"
	}
	postgresTag := envVars["TM_PG_TAG"]
	if postgresTag == "" {
		postgresTag = "18-trixie"
	}
	timezone := envVars["TM_TZ"]
	if timezone == "" {
		timezone = "UTC"
	}
	grafanaAdminUser := envVars["GRAFANA_USER"]
	if grafanaAdminUser == "" {
		grafanaAdminUser = "admin"
	}
	bindAddress := envVars["TM_BIND"]
	if bindAddress == "" {
		bindAddress = "0.0.0.0"
	}
	region := in.Region
	if region == "" {
		region = envVars["TM_REGION"]
	}
	if region == "" {
		region = "global"
	}
	disableMQTT := envVars["TM_DISABLE_MQTT"] == "true"

	// ExtraEnv: non-standard keys from .env are preserved.
	extraEnv := map[string]string{}
	for k, v := range envVars {
		if !isManagedEnvKey(k) {
			extraEnv[k] = v
		}
	}

	projectName := compose.ProjectName(in.Slug)
	dbName := envVars["TM_DB_NAME"]
	if dbName == "" {
		dbName = "teslamate_" + strings.ReplaceAll(in.Slug, "-", "_")
	}
	inst := &model.Instance{
		ID:               ids.New(),
		Name:             in.Name,
		Slug:             in.Slug,
		HostID:           in.HostID,
		ProjectName:      projectName,
		BaseDir:          in.BaseDir,
		BindAddress:      bindAddress,
		TMPort:           tmPort,
		GrafanaPort:      grafanaPort,
		MQTTPort:         mqttPort,
		Timezone:         timezone,
		Region:           region,
		ImageTag:         imageTag,
		PostgresTag:      postgresTag,
		GrafanaAdminUser: grafanaAdminUser,
		DisableMQTT:      disableMQTT,
		ExtraEnv:         extraEnv,
		DBUser:           "teslamate",
		DBName:           dbName,
		ComposeMajor:     major,
		Status:           model.InstCreated,
	}

	wrapped, err := s.wrapInstanceSecrets(inst.ID, encKey, dbPass, grafanaPass)
	if err != nil {
		return nil, err
	}
	inst.TMEncryptionKey, inst.DBPass, inst.GrafanaPass = wrapped.enc, wrapped.db, wrapped.grafana

	if err := s.Store.CreateInstance(inst); err != nil {
		return nil, err
	}

	// Check remote container status to set initial instance status (import
	// reflects reality: if containers are already up, mark as running).
	cmdBits, ok := compose.Command(caps.ComposeKind)
	if ok {
		psCmd := fmt.Sprintf("cd %s && %s%s --env-file %s -f %s -p %s ps --format json 2>/dev/null",
			sshx.Quote(in.BaseDir), dockerPrefix(sshx.Sudo{}, caps), cmdBits,
			sshx.Quote(in.BaseDir+"/.env"), sshx.Quote(composePath),
			sshx.Quote(inst.ProjectName))
		res, err := client.Run(context.Background(), psCmd, sshx.RunOptions{})
		if err == nil {
			states := parseComposePS(res.Stdout, major)
			if status := aggregateStatus(states); status != model.InstUnknown {
				inst.Status = status
				_ = s.Store.SetInstanceStatus(inst.ID, status, "", "")
			}
		}
	}

	return s.LoadInstanceSecrets(inst)
}

// parseEnvFile parses a simple KEY=VALUE .env file into a map.
func parseEnvFile(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx >= 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			val = strings.Trim(val, `"`)
			out[key] = val
		}
	}
	return out
}

// isManagedEnvKey reports whether an environment variable is owned by 1mate
// (and thus should not be copied into ExtraEnv).
func isManagedEnvKey(k string) bool {
	switch k {
	case "TM_ENCRYPTION_KEY", "ENCRYPTION_KEY", "TM_DB_USER", "TM_DB_PASS", "DATABASE_PASS", "TM_DB_NAME",
		"GRAFANA_USER", "GRAFANA_PW", "TM_TZ", "TM_TM_PORT", "TM_GRAFANA_PORT",
		"TM_MQTT_PORT", "TM_BIND", "TM_IMAGE_TAG", "TM_PG_TAG", "TM_REGION",
		"TM_API_HOST", "TM_AUTH_HOST", "TM_WSS_HOST", "TM_DISABLE_MQTT":
		return true
	}
	return false
}

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
		DBName:           "teslamate_" + strings.ReplaceAll(in.Slug, "-", "_"),
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
	if inst.TMEncryptionKey == "" || inst.DBPass == "" || inst.GrafanaPass == "" {
		return fmt.Errorf("%w: instance secrets are empty; instance may have been created with a different master key", ErrValidation)
	}
	var err error
	if inst.TMEncryptionKey, err = s.Box.Decrypt(inst.TMEncryptionKey, "instances:"+inst.ID+":encryption_key"); err != nil {
		return fmt.Errorf("%w: cannot decrypt encryption_key: %v", ErrValidation, err)
	}
	if inst.DBPass, err = s.Box.Decrypt(inst.DBPass, "instances:"+inst.ID+":db_pass"); err != nil {
		return fmt.Errorf("%w: cannot decrypt db_pass: %v", ErrValidation, err)
	}
	if inst.GrafanaPass, err = s.Box.Decrypt(inst.GrafanaPass, "instances:"+inst.ID+":grafana_pass"); err != nil {
		return fmt.Errorf("%w: cannot decrypt grafana_pass: %v", ErrValidation, err)
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
	// Load from store directly to preserve ciphertext secrets (avoid GetInstance
	// which decrypts secrets into plaintext before storing).
	instDB, err := s.Store.GetInstance(id)
	if err != nil {
		return nil, err
	}
	in.Normalize()
	if in.HostID == "" {
		in.HostID = instDB.HostID
	}
	if in.HostID != instDB.HostID {
		return nil, fmt.Errorf("%w: changing the host of an instance is not supported; create a new instance instead", ErrValidation)
	}
	if in.Slug == "" {
		in.Slug = instDB.Slug
	}
	if in.Slug != instDB.Slug {
		return nil, fmt.Errorf("%w: the slug cannot be changed after creation", ErrValidation)
	}
	if in.Name == "" {
		in.Name = instDB.Name
	}
	if in.TMPort == 0 {
		in.TMPort = instDB.TMPort
	}
	if in.GrafanaPort == 0 {
		in.GrafanaPort = instDB.GrafanaPort
	}
	if in.MQTTPort == 0 {
		in.MQTTPort = instDB.MQTTPort
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}

	holders, err := s.Store.PortHolders(instDB.HostID, instDB.ID)
	if err != nil {
		return nil, err
	}
	for _, p := range []int{in.TMPort, in.GrafanaPort, in.MQTTPort} {
		if other, taken := holders[p]; taken {
			return nil, fmt.Errorf("%w: port %d is already used by another instance on this host (%s)", ErrValidation, p, other)
		}
	}
	portsChanged := in.TMPort != instDB.TMPort || in.GrafanaPort != instDB.GrafanaPort || in.MQTTPort != instDB.MQTTPort
	if portsChanged {
		if caps, err := s.Store.GetCapabilities(instDB.HostID); err == nil {
			for _, p := range []int{in.TMPort, in.GrafanaPort, in.MQTTPort} {
				if containsInt(caps.PortsInUse, p) && !containsInt([]int{instDB.TMPort, instDB.GrafanaPort, instDB.MQTTPort}, p) {
					return nil, fmt.Errorf("%w: port %d is already listening on the host", ErrValidation, p)
				}
			}
		}
	}

	baseDir := in.BaseDir
	if baseDir == "" {
		baseDir = instDB.BaseDir
	}

	instDB.Name = in.Name
	instDB.BaseDir = baseDir
	instDB.BindAddress = in.BindAddress
	instDB.TMPort = in.TMPort
	instDB.GrafanaPort = in.GrafanaPort
	instDB.MQTTPort = in.MQTTPort
	instDB.Timezone = in.Timezone
	instDB.Region = in.Region
	instDB.ImageTag = in.ImageTag
	instDB.PostgresTag = in.PostgresTag
	instDB.GrafanaAdminUser = in.GrafanaAdminUser
	instDB.DisableMQTT = in.DisableMQTT
	if in.ExtraEnv != nil {
		instDB.ExtraEnv = in.ExtraEnv
	}
	if err := s.Store.UpdateInstance(instDB); err != nil {
		return nil, err
	}
	return s.LoadInstanceSecrets(instDB)
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
	client      sshx.Client
	sudo        sshx.Sudo
	prefix      string
	major       int
	baseDir     string
	project     string
	envFile     string
	composeFile string // detected compose file name: compose.yaml | compose.yml | docker-compose.yml
	argv        string
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
	if inst.ComposeMajor != 0 {
		major = inst.ComposeMajor
	}
	if inst.ComposeMajor != 0 && inst.ComposeMajor != major {
		// Respect the major version the instance was created with; if the host
		// lost that form, fail loudly instead of silently switching syntax.
		if inst.ComposeMajor == 1 && caps.ComposeKind != model.ComposeStandaloneV1 {
			s.closeQuietly(client)
			return nil, fmt.Errorf("%w: this instance was deployed with Compose v1 but the host now has %s; redeploy to migrate", ErrValidation, caps.ComposeKind)
		}
	}
	// Detect which compose file actually exists on the remote host.
	composeFile := "compose.yaml"
	for _, candidate := range []string{"compose.yaml", "compose.yml", "docker-compose.yml"} {
		testCmd := fmt.Sprintf("test -f %s", sshx.Quote(inst.BaseDir+"/"+candidate))
		res, err := client.Run(ctx, testCmd, sshx.RunOptions{})
		if err == nil && res.ExitCode == 0 {
			composeFile = candidate
			break
		}
	}
	return &composeRunner{
		client:      client,
		sudo:        sudo,
		prefix:      dockerPrefix(sudo, caps),
		major:       major,
		baseDir:     inst.BaseDir,
		project:     inst.ProjectName,
		envFile:     inst.BaseDir + "/.env",
		composeFile: composeFile,
		argv:        cmdBits,
	}, nil
}

func (r *composeRunner) Close() { r.client.Close() }

// cmd builds "cd <dir> && <compose> --env-file <env> -f <composeFile> -p <project> <args>"
func (r *composeRunner) cmd(args string) string {
	return fmt.Sprintf("cd %s && %s%s --env-file %s -f %s -p %s %s",
		sshx.Quote(r.baseDir), r.prefix, r.argv, sshx.Quote(r.envFile),
		sshx.Quote(r.baseDir+"/"+r.composeFile), sshx.Quote(r.project), args)
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
