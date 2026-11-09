import { useMemo, useState } from 'react'
import { Button, Descriptions, Empty, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import type { Capabilities, Host } from '../api/types'
import { ComposeKindTag, HostStatusTag } from './StatusTags'
import { formatBytes, formatDateTime, formatDuration } from '../utils/format'

interface Props {
  host: Host
  capabilities?: Capabilities
  onProbe?: () => void
  probing?: boolean
}

function YesNo({ value }: { value: boolean }) {
  return value ? <Tag color="green">是</Tag> : <Tag>否</Tag>
}

function diskFree(kb: number): string {
  return formatBytes(kb * 1024)
}

export default function CapabilityPanel({ host, capabilities, onProbe, probing }: Props) {
  const [showPorts, setShowPorts] = useState(false)

  const ports = useMemo(() => capabilities?.ports_in_use ?? [], [capabilities])

  if (!capabilities) {
    return (
      <Empty
        description={
          <Space direction="vertical">
            <Typography.Text type="secondary">尚未获取该主机的环境信息</Typography.Text>
            <Button type="primary" icon={<ReloadOutlined />} loading={probing} onClick={onProbe}>
              立即探测
            </Button>
          </Space>
        }
      />
    )
  }

  return (
    <Space direction="vertical" style={{ width: '100%' }} size="middle">
      {capabilities.unsupported_reason && (
        <Typography.Text type="danger">{capabilities.unsupported_reason}</Typography.Text>
      )}
      {capabilities.docker_socket_access === 'denied' && (
        <Typography.Text type="warning">
          SSH 用户无法访问 Docker socket。可配置 sudo 提权，或将用户加入 docker 组（需要重新登录）。
        </Typography.Text>
      )}
      {capabilities.docker_rootless && (
        <Typography.Text type="warning">
          检测到 rootless Docker：低于 1024 的端口可能无法绑定，卷路径也与 root 模式不同。
        </Typography.Text>
      )}

      <Descriptions bordered size="small" column={{ xs: 1, sm: 2, lg: 3 }}>
        <Descriptions.Item label="主机状态">
          <HostStatusTag status={host.status} />
        </Descriptions.Item>
        <Descriptions.Item label="系统">
          {capabilities.os_pretty || `${capabilities.os_id} ${capabilities.os_version}`}
        </Descriptions.Item>
        <Descriptions.Item label="架构">{capabilities.arch || '未知'}</Descriptions.Item>
        <Descriptions.Item label="内核">{capabilities.kernel || '未知'}</Descriptions.Item>
        <Descriptions.Item label="Init 系统">{capabilities.init_system || '未知'}</Descriptions.Item>
        <Descriptions.Item label="CPU / 内存">
          {capabilities.cpu_count} 核 / {capabilities.mem_mb} MB
        </Descriptions.Item>
        <Descriptions.Item label="磁盘可用 /opt">{diskFree(capabilities.disk_free_kb)}</Descriptions.Item>
        <Descriptions.Item label="Docker 已安装">
          <YesNo value={capabilities.docker_present} />
        </Descriptions.Item>
        <Descriptions.Item label="Docker 守护进程">
          <YesNo value={capabilities.docker_running} />
        </Descriptions.Item>
        <Descriptions.Item label="Docker 客户端">
          {capabilities.docker_client_version || '—'}
        </Descriptions.Item>
        <Descriptions.Item label="Docker 服务端">
          {capabilities.docker_server_version || '—'}
        </Descriptions.Item>
        <Descriptions.Item label="Socket 访问">{capabilities.docker_socket_access}</Descriptions.Item>
        <Descriptions.Item label="Docker 需 sudo">
          <YesNo value={capabilities.docker_sudo} />
        </Descriptions.Item>
        <Descriptions.Item label="Compose 类型">
          <ComposeKindTag kind={capabilities.compose_kind} />
        </Descriptions.Item>
        <Descriptions.Item label="Compose 版本">
          {capabilities.compose_version || '—'}
          {capabilities.compose_major === 1 && (
            <Tooltip title="Compose v1 使用 docker-compose 命令，已停止维护">
              <Tag color="gold" style={{ marginLeft: 6 }}>
                旧
              </Tag>
            </Tooltip>
          )}
        </Descriptions.Item>
        <Descriptions.Item label="Compose 路径">
          <span className="mono">{capabilities.compose_path || '—'}</span>
        </Descriptions.Item>
        <Descriptions.Item label="v1 二进制存在">
          <YesNo value={capabilities.compose_v1_binary_present} />
        </Descriptions.Item>
        <Descriptions.Item label="最近探测">
          {capabilities.probed_at ? formatDateTime(capabilities.probed_at) : '—'}
        </Descriptions.Item>
        <Descriptions.Item label="已监听端口">
          {ports.length === 0 ? (
            '无'
          ) : (
            <Space wrap>
              {ports.slice(0, showPorts ? ports.length : 8).map((p) => (
                <Tag key={p}>{p}</Tag>
              ))}
              {ports.length > 8 && (
                <Button type="link" size="small" onClick={() => setShowPorts((v) => !v)}>
                  {showPorts ? '收起' : `+${ports.length - 8}`}
                </Button>
              )}
            </Space>
          )}
        </Descriptions.Item>
      </Descriptions>

      <Table
        size="small"
        pagination={false}
        rowKey={(r) => r.name}
        dataSource={capabilityChecks(capabilities)}
        columns={[
          { title: '检查项', dataIndex: 'name' },
          {
            title: '结果',
            dataIndex: 'ok',
            render: (ok: boolean, row) =>
              ok ? <Tag color="green">通过</Tag> : <Tag color="red">{row.problem}</Tag>,
          },
        ]}
      />

      {host.status_message && (
        <Typography.Text type="secondary">状态说明：{host.status_message}</Typography.Text>
      )}
      {probing && <Typography.Text type="secondary">探测中…</Typography.Text>}
      <Typography.Text type="secondary" className="muted">
        探测耗时 {capabilities.probed_at ? formatDuration(Date.now() - new Date(capabilities.probed_at).getTime()) : '—'}前
      </Typography.Text>
    </Space>
  )
}

function capabilityChecks(c: Capabilities) {
  return [
    { name: '64 位架构', ok: !c.unsupported_reason, problem: c.unsupported_reason || '' },
    { name: 'Docker 已安装', ok: c.docker_present, problem: '未安装 docker' },
    { name: 'Docker 可访问', ok: c.docker_running, problem: '无法访问 docker daemon' },
    { name: 'Compose 可用', ok: c.compose_kind !== 'none', problem: '未安装 compose' },
    {
      name: 'Compose v2 优先',
      ok: c.compose_major === 2,
      problem: '仅 v1，建议安装 v2',
    },
    { name: '磁盘 ≥ 2GB', ok: c.disk_free_kb >= 2 * 1024 * 1024, problem: '磁盘空间不足' },
    { name: '内存 ≥ 2GB', ok: c.mem_mb >= 2048, problem: '内存可能不足（最低 1GB）' },
  ]
}
