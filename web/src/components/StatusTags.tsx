import { Tag, Tooltip } from 'antd'
import type { ComposeKind, HostStatus, InstanceStatus, JobStatus } from '../api/types'

const hostStatusMeta: Record<HostStatus, { color: string; label: string; hint?: string }> = {
  unknown: { color: 'default', label: '未知', hint: '尚未探测' },
  ok: { color: 'success', label: '可用' },
  unreachable: { color: 'error', label: '不可达', hint: 'SSH 连接失败' },
  denied: { color: 'error', label: '认证失败', hint: 'SSH 认证或权限被拒绝' },
  host_key_changed: { color: 'volcano', label: '主机密钥变更', hint: '指纹与固定值不一致' },
  no_docker: { color: 'warning', label: '未安装 Docker' },
  no_compose: { color: 'warning', label: '未安装 Compose' },
  compose_v1: { color: 'gold', label: '仅 Compose v1', hint: 'v1 已停止维护，建议升级到 v2' },
  installing: { color: 'processing', label: '安装中' },
  unsupported: { color: 'default', label: '不支持' },
  docker_socket_denied: { color: 'warning', label: 'Docker 无权限' },
}

const instanceStatusMeta: Record<InstanceStatus, { color: string; label: string }> = {
  created: { color: 'default', label: '未部署' },
  deploying: { color: 'processing', label: '部署中' },
  running: { color: 'success', label: '运行中' },
  degraded: { color: 'warning', label: '部分异常' },
  stopped: { color: 'default', label: '已停止' },
  failed: { color: 'error', label: '失败' },
  interrupted: { color: 'volcano', label: '已中断' },
  deleted: { color: 'default', label: '已删除' },
  unknown: { color: 'default', label: '未知' },
}

const jobStatusMeta: Record<JobStatus, { color: string; label: string }> = {
  queued: { color: 'default', label: '排队中' },
  running: { color: 'processing', label: '执行中' },
  succeeded: { color: 'success', label: '成功' },
  failed: { color: 'error', label: '失败' },
  canceled: { color: 'default', label: '已取消' },
  interrupted: { color: 'volcano', label: '已中断' },
}

export function HostStatusTag({ status }: { status: HostStatus }) {
  const meta = hostStatusMeta[status] ?? hostStatusMeta.unknown
  const tag = (
    <Tag color={meta.color} bordered={false}>
      {meta.label}
    </Tag>
  )
  return meta.hint ? <Tooltip title={meta.hint}>{tag}</Tooltip> : tag
}

export function InstanceStatusTag({ status }: { status: InstanceStatus }) {
  const meta = instanceStatusMeta[status] ?? instanceStatusMeta.unknown
  return (
    <Tag color={meta.color} bordered={false}>
      {meta.label}
    </Tag>
  )
}

export function JobStatusTag({ status }: { status: JobStatus }) {
  const meta = jobStatusMeta[status] ?? jobStatusMeta.queued
  return (
    <Tag color={meta.color} bordered={false}>
      {meta.label}
    </Tag>
  )
}

const composeMeta: Record<ComposeKind, { color: string; label: string }> = {
  none: { color: 'default', label: '未安装' },
  plugin_v2: { color: 'success', label: 'v2 插件' },
  standalone_v2: { color: 'success', label: 'v2 独立版' },
  standalone_v1: { color: 'gold', label: 'v1 独立版（已 EOL）' },
}

export function ComposeKindTag({ kind }: { kind: ComposeKind }) {
  const meta = composeMeta[kind] ?? composeMeta.none
  return (
    <Tag color={meta.color} bordered={false}>
      {meta.label}
    </Tag>
  )
}

export function jobKindLabel(kind: string): string {
  const labels: Record<string, string> = {
    host_probe: '主机探测',
    install_docker: '安装 Docker',
    deploy: '部署',
    start: '启动',
    stop: '停止',
    restart: '重启',
    upgrade: '升级',
    backup: '备份',
    restore: '恢复',
    destroy: '销毁',
    reconcile: '状态对账',
  }
  return labels[kind] ?? kind
}
