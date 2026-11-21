import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, uploadFile } from './client'
import type {
  AuditLog,
  Backup,
  Capabilities,
  ComposePreviewData,
  Host,
  ImportInstanceInput,
  Instance,
  Job,
  MeResponse,
  SetupStatus,
} from './types'

export function useSetupStatus() {
  return useQuery({
    queryKey: ['setup'],
    queryFn: () => api.get<SetupStatus>('/api/v1/auth/setup'),
    staleTime: 30_000,
  })
}

export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: () => api.get<MeResponse>('/api/v1/auth/me'),
    retry: false,
    staleTime: 5 * 60_000,
  })
}

export function useHosts() {
  return useQuery({
    queryKey: ['hosts'],
    queryFn: () => api.get<{ hosts: Host[] }>('/api/v1/hosts'),
    refetchInterval: 15_000,
  })
}

export function useHost(id: string | undefined) {
  return useQuery({
    queryKey: ['host', id],
    queryFn: () => api.get<{ host: Host; capabilities?: Capabilities }>(`/api/v1/hosts/${id}`),
    enabled: Boolean(id),
    refetchInterval: 15_000,
  })
}

export function useInstances(hostId?: string) {
  const qs = hostId ? `?host_id=${encodeURIComponent(hostId)}` : ''
  return useQuery({
    queryKey: ['instances', hostId ?? 'all'],
    queryFn: () => api.get<{ instances: Instance[] }>(`/api/v1/instances${qs}`),
    refetchInterval: 15_000,
  })
}

export function useInstance(id: string | undefined) {
  return useQuery({
    queryKey: ['instance', id],
    queryFn: () => api.get<Instance>(`/api/v1/instances/${id}`),
    enabled: Boolean(id),
    refetchInterval: 10_000,
  })
}

export function useJobs(filter: { host_id?: string; instance_id?: string } = {}) {
  const params = new URLSearchParams()
  if (filter.host_id) params.set('host_id', filter.host_id)
  if (filter.instance_id) params.set('instance_id', filter.instance_id)
  const qs = params.toString()
  return useQuery({
    queryKey: ['jobs', qs],
    queryFn: () => api.get<{ jobs: Job[] }>(`/api/v1/jobs${qs ? `?${qs}` : ''}`),
    refetchInterval: 5_000,
  })
}

export function useJob(id: string | undefined) {
  return useQuery({
    queryKey: ['job', id],
    queryFn: () => api.get<{ job: Job; log: string }>(`/api/v1/jobs/${id}`),
    enabled: Boolean(id),
  })
}

export function useBackups(instanceId: string | undefined) {
  return useQuery({
    queryKey: ['backups', instanceId],
    queryFn: () => api.get<{ backups: Backup[] }>(`/api/v1/instances/${instanceId}/backups`),
    enabled: Boolean(instanceId),
  })
}

export function useComposePreview(instanceId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['compose', instanceId],
    queryFn: () => api.get<ComposePreviewData>(`/api/v1/instances/${instanceId}/compose`),
    enabled: Boolean(instanceId) && enabled,
  })
}

export function useAuditLogs() {
  return useQuery({
    queryKey: ['audit'],
    queryFn: () => api.get<{ logs: AuditLog[] }>('/api/v1/audit-logs'),
    refetchInterval: 30_000,
  })
}

export function useSettings() {
  return useQuery({
    queryKey: ['settings'],
    queryFn: () => api.get<{ settings: Record<string, string> }>('/api/v1/settings'),
  })
}

export function useUpdateSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (settings: Record<string, string>) =>
      api.patch<{ settings: Record<string, string> }>('/api/v1/settings', settings),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings'] }),
  })
}

export function useHostAction() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, action }: { id: string; action: 'probe' }) =>
      api.post<{ job_id: string }>(`/api/v1/hosts/${id}/actions/${action}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['hosts'] }),
  })
}

export interface InstallRequest {
  strategy: 'repo' | 'compose_only' | 'script'
  docker_version?: string
  mirror?: string
  compose_version?: string
  registry_mirrors?: string[]
  accept_system_changes: boolean
  save_sudo_password?: string
}

export function useInstallDocker(hostId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (req: InstallRequest) =>
      api.post<{ job_id: string }>(`/api/v1/hosts/${hostId}/actions/install-docker`, req),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['host', hostId] })
      void qc.invalidateQueries({ queryKey: ['hosts'] })
    },
  })
}

export function useInstallScript(hostId: string, params: Record<string, string>) {
  const search = new URLSearchParams(params).toString()
  return useQuery({
    queryKey: ['install-script', hostId, search],
    queryFn: () => api.get<import('./types').InstallScriptResponse>(`/api/v1/hosts/${hostId}/install-script?${search}`),
    enabled: Boolean(hostId),
  })
}

export type InstanceAction =
  | 'deploy'
  | 'start'
  | 'stop'
  | 'restart'
  | 'upgrade'
  | 'reconcile'
  | 'backup'
  | 'restore'
  | 'destroy'

export function useInstanceAction(instanceId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ action, body }: { action: InstanceAction; body?: Record<string, unknown> }) =>
      api.post<{ job_id: string }>(`/api/v1/instances/${instanceId}/actions/${action}`, body ?? {}),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['instance', instanceId] })
      void qc.invalidateQueries({ queryKey: ['jobs'] })
    },
  })
}

export function useCreateInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.post<Instance>('/api/v1/instances', payload),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}

export function useUpdateInstance(id: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.patch<Instance>(`/api/v1/instances/${id}`, payload),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['instance', id] })
      void qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
}

export function useUploadBackup(instanceId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ file, note }: { file: File; note?: string }) =>
      uploadFile<Backup>(`/api/v1/instances/${instanceId}/backups`, file, note ? { note } : {}),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backups', instanceId] }),
  })
}

export function useImportInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (payload: ImportInstanceInput) =>
      api.post<Instance>('/api/v1/instances/import', payload),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}

export function useChangePassword() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (payload: { old_password: string; new_password: string }) =>
      api.post<{ ok: boolean }>('/api/v1/auth/change-password', payload),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['me'] })
    },
  })
}
