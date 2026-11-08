package model

import "time"

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Session struct {
	ID         string
	UserID     string
	TokenHash  string
	CSRFToken  string
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

type CredentialKind string

const (
	CredSSHKey      CredentialKind = "ssh_key"
	CredSSHPassword CredentialKind = "ssh_password"
	CredSudoPass    CredentialKind = "sudo_password"
)

type Credential struct {
	ID                   string         `json:"id"`
	Kind                 CredentialKind `json:"kind"`
	Ciphertext           string         `json:"-"`
	PassphraseCiphertext string         `json:"-"`
	KeyVersion           int            `json:"key_version"`
	Comment              string         `json:"comment"`
	CreatedAt            time.Time      `json:"created_at"`
}

type HostAuthType string

const (
	HostAuthKey      HostAuthType = "key"
	HostAuthPassword HostAuthType = "password"
)

type HostStatus string

const (
	HostUnknown       HostStatus = "unknown"
	HostOK            HostStatus = "ok"
	HostUnreachable   HostStatus = "unreachable"
	HostDenied        HostStatus = "denied"
	HostKeyChanged    HostStatus = "host_key_changed"
	HostNoDocker      HostStatus = "no_docker"
	HostNoCompose     HostStatus = "no_compose"
	HostComposeV1     HostStatus = "compose_v1"
	HostInstalling    HostStatus = "installing"
	HostUnsupported   HostStatus = "unsupported"
	HostDockerDenied  HostStatus = "docker_socket_denied"
)

type SudoMode string

const (
	SudoNone     SudoMode = "none"
	SudoRoot     SudoMode = "root"
	SudoNopasswd SudoMode = "nopasswd"
	SudoPassword SudoMode = "password"
)

type Host struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Address           string       `json:"address"`
	SSHPort           int          `json:"ssh_port"`
	SSHUser           string       `json:"ssh_user"`
	AuthType          HostAuthType `json:"auth_type"`
	CredentialID      string       `json:"credential_id"`
	HostKeyFingerpr   string       `json:"host_key_fingerprint"`
	SudoMode          SudoMode     `json:"sudo_mode"`
	SudoCredentialID  string       `json:"sudo_credential_id"`
	Status            HostStatus   `json:"status"`
	StatusMessage     string       `json:"status_message"`
	LastProbeAt       *time.Time   `json:"last_probe_at"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`

	// Populated for read responses only.
	HasCredential bool `json:"has_credential"`
	HasSudoPass   bool `json:"has_sudo_password"`
}

type ComposeKind string

const (
	ComposeNone         ComposeKind = "none"
	ComposePluginV2     ComposeKind = "plugin_v2"
	ComposeStandaloneV2 ComposeKind = "standalone_v2"
	ComposeStandaloneV1 ComposeKind = "standalone_v1"
)

func (k ComposeKind) Major() int {
	switch k {
	case ComposePluginV2, ComposeStandaloneV2:
		return 2
	case ComposeStandaloneV1:
		return 1
	}
	return 0
}

func (k ComposeKind) Usable() bool { return k.Major() > 0 }

type SocketAccess string

const (
	SocketAbsent SocketAccess = "absent"
	SocketOK     SocketAccess = "ok"
	SocketDenied SocketAccess = "denied"
)

type Capabilities struct {
	HostID       string      `json:"host_id"`
	OSID         string      `json:"os_id"`
	OSVersion    string      `json:"os_version"`
	OSCodename   string      `json:"os_codename"`
	OSPretty     string      `json:"os_pretty"`
	Arch         string      `json:"arch"`
	Kernel       string      `json:"kernel"`
	InitSystem   string      `json:"init_system"`
	Cgroup       string      `json:"cgroup_version"`
	CPUCount     int         `json:"cpu_count"`
	MemMB        int         `json:"mem_mb"`
	DiskFreeKB   int64       `json:"disk_free_kb"`
	DockerPresent bool       `json:"docker_present"`
	DockerClient string      `json:"docker_client_version"`
	DockerServer string      `json:"docker_server_version"`
	DockerRunning bool       `json:"docker_running"`
	SocketAccess SocketAccess `json:"docker_socket_access"`
	DockerSudo   bool        `json:"docker_sudo"`
	DockerRootless bool      `json:"docker_rootless"`
	ComposeKind  ComposeKind `json:"compose_kind"`
	ComposeVersion string    `json:"compose_version"`
	ComposeMajor int         `json:"compose_major"`
	ComposePath  string      `json:"compose_path"`
	ComposeV1Present bool    `json:"compose_v1_binary_present"`
	PortsInUse   []int       `json:"ports_in_use"`
	UnsupportedReason string `json:"unsupported_reason"`
	RawJSON      string      `json:"-"`
	ProbedAt     time.Time   `json:"probed_at"`
}

type InstanceStatus string

const (
	InstCreated     InstanceStatus = "created"
	InstDeploying   InstanceStatus = "deploying"
	InstRunning     InstanceStatus = "running"
	InstDegraded    InstanceStatus = "degraded"
	InstStopped     InstanceStatus = "stopped"
	InstFailed      InstanceStatus = "failed"
	InstInterrupted InstanceStatus = "interrupted"
	InstDeleted     InstanceStatus = "deleted"
	InstUnknown     InstanceStatus = "unknown"
)

type Instance struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Slug             string         `json:"slug"`
	HostID           string         `json:"host_id"`
	ProjectName      string         `json:"project_name"`
	BaseDir          string         `json:"base_dir"`
	BindAddress      string         `json:"bind_address"`
	TMPort           int            `json:"tm_port"`
	GrafanaPort      int            `json:"grafana_port"`
	MQTTPort         int            `json:"mqtt_port"`
	Timezone         string         `json:"timezone"`
	Region           string         `json:"region"`
	ImageTag         string         `json:"image_tag"`
	PostgresTag      string         `json:"postgres_tag"`
	GrafanaAdminUser string         `json:"grafana_admin_user"`
	DisableMQTT      bool           `json:"disable_mqtt"`
	ExtraEnv         map[string]string `json:"extra_env"`
	TMEncryptionKey  string         `json:"-"`
	DBUser           string         `json:"db_user"`
	DBPass           string         `json:"-"`
	GrafanaPass      string         `json:"-"`
	ComposeMajor     int            `json:"compose_major"`
	ComposeSHA256    string         `json:"compose_sha256"`
	Status           InstanceStatus `json:"status"`
	StatusMessage    string         `json:"status_message"`
	LastJobID        string         `json:"last_job_id"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type JobKind string

const (
	JobHostProbe    JobKind = "host_probe"
	JobInstallDocker JobKind = "install_docker"
	JobDeploy       JobKind = "deploy"
	JobStart        JobKind = "start"
	JobStop         JobKind = "stop"
	JobRestart      JobKind = "restart"
	JobUpgrade      JobKind = "upgrade"
	JobBackup       JobKind = "backup"
	JobRestore      JobKind = "restore"
	JobDestroy      JobKind = "destroy"
	JobReconcile    JobKind = "reconcile"
)

type JobStatus string

const (
	JobQueued      JobStatus = "queued"
	JobRunning     JobStatus = "running"
	JobSucceeded   JobStatus = "succeeded"
	JobFailed      JobStatus = "failed"
	JobCanceled    JobStatus = "canceled"
	JobInterrupted JobStatus = "interrupted"
)

func (s JobStatus) Terminal() bool {
	switch s {
	case JobSucceeded, JobFailed, JobCanceled, JobInterrupted:
		return true
	}
	return false
}

type Job struct {
	ID         string    `json:"id"`
	Kind       JobKind   `json:"kind"`
	HostID     string    `json:"host_id"`
	InstanceID string    `json:"instance_id"`
	Status     JobStatus `json:"status"`
	Stage      string    `json:"stage"`
	ExitCode   int       `json:"exit_code"`
	Error      string    `json:"error"`
	Payload    string    `json:"-"`
	LogPath    string    `json:"-"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type Backup struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	Filename   string    `json:"filename"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	Format     string    `json:"format"`
	Note       string    `json:"note"`
	CreatedAt  time.Time `json:"created_at"`
}

type AuditLog struct {
	ID         string    `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	DetailJSON string    `json:"detail"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"created_at"`
}

// DefaultComposeVersion is the pinned Compose v2 release installed when a host
// has no usable Compose. Pinning avoids depending on the GitHub releases API.
const DefaultComposeVersion = "2.29.7"

// SupportedComposeMajor reports whether the platform can orchestrate a host
// with the given Compose major version.
func SupportedComposeMajor(major int) bool { return major == 1 || major == 2 }
