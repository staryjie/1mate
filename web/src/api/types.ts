export type HostStatus =
  | 'unknown'
  | 'ok'
  | 'unreachable'
  | 'denied'
  | 'host_key_changed'
  | 'no_docker'
  | 'no_compose'
  | 'compose_v1'
  | 'installing'
  | 'unsupported'
  | 'docker_socket_denied'

export type ComposeKind = 'none' | 'plugin_v2' | 'standalone_v2' | 'standalone_v1'

export type InstanceStatus =
  | 'created'
  | 'deploying'
  | 'running'
  | 'degraded'
  | 'stopped'
  | 'failed'
  | 'interrupted'
  | 'deleted'
  | 'unknown'

export type JobStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'interrupted'

export type JobKind =
  | 'host_probe'
  | 'install_docker'
  | 'deploy'
  | 'start'
  | 'stop'
  | 'restart'
  | 'upgrade'
  | 'backup'
  | 'restore'
  | 'destroy'
  | 'reconcile'

export interface Capabilities {
  host_id: string
  os_id: string
  os_version: string
  os_codename: string
  os_pretty: string
  arch: string
  kernel: string
  init_system: string
  cgroup_version: string
  cpu_count: number
  mem_mb: number
  disk_free_kb: number
  docker_present: boolean
  docker_client_version: string
  docker_server_version: string
  docker_running: boolean
  docker_socket_access: 'absent' | 'ok' | 'denied'
  docker_sudo: boolean
  docker_rootless: boolean
  compose_kind: ComposeKind
  compose_version: string
  compose_major: number
  compose_path: string
  compose_v1_binary_present: boolean
  ports_in_use: number[]
  unsupported_reason: string
  probed_at: string
}

export interface Host {
  id: string
  name: string
  address: string
  ssh_port: number
  ssh_user: string
  auth_type: 'key' | 'password'
  credential_id: string
  host_key_fingerprint: string
  sudo_mode: 'none' | 'root' | 'nopasswd' | 'password'
  sudo_credential_id: string
  status: HostStatus
  status_message: string
  last_probe_at: string | null
  created_at: string
  updated_at: string
  has_credential: boolean
  has_sudo_password: boolean
  capabilities?: Capabilities
  instance_count?: number
}

export interface Instance {
  id: string
  name: string
  slug: string
  host_id: string
  project_name: string
  base_dir: string
  bind_address: string
  tm_port: number
  grafana_port: number
  mqtt_port: number
  timezone: string
  region: 'global' | 'cn'
  image_tag: string
  postgres_tag: string
  grafana_admin_user: string
  disable_mqtt: boolean
  extra_env: Record<string, string>
  db_user: string
  compose_major: number
  compose_sha256: string
  status: InstanceStatus
  status_message: string
  last_job_id: string
  created_at: string
  updated_at: string
  has_encryption_key?: boolean
}

export interface Job {
  id: string
  kind: JobKind
  host_id: string
  instance_id: string
  status: JobStatus
  stage: string
  exit_code: number
  error: string
  created_by: string
  created_at: string
  started_at: string | null
  finished_at: string | null
}

export interface Backup {
  id: string
  instance_id: string
  filename: string
  size_bytes: number
  sha256: string
  format: string
  note: string
  created_at: string
}

export interface AuditLog {
  id: string
  actor: string
  action: string
  target_type: string
  target_id: string
  detail: string
  ip: string
  created_at: string
}

export interface User {
  id: string
  username: string
  created_at: string
  updated_at: string
}

export interface SetupStatus {
  setup_required: boolean
  version: string
}

export interface MeResponse {
  user: User
  csrf_token: string
  version: string
}

export interface ConnectionTest {
  ok: boolean
  fingerprint: string
  message: string
  docker: string
}

export interface InstallScriptResponse {
  script: string
  options: {
    strategy: string
    mirror: string
    docker_version: string
    compose_version: string
  }
}

export interface ComposePreviewData {
  compose: string
  env: string
  compose_kind: ComposeKind
  command_prefix: string
  project_name: string
  base_dir: string
}

export interface ImportInstanceInput {
  name: string
  slug: string
  host_id: string
  base_dir: string
  compose_major?: number
  tm_port?: number
  grafana_port?: number
  mqtt_port?: number
  region?: string
}
