import { useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Modal,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  message,
} from 'antd'
import { SwapOutlined } from '@ant-design/icons'
import { useHosts, useImagePrecheck, useImageSync } from '../api/hooks'
import type { ImagePrecheckResult } from '../api/types'

// 镜像同步面板：检查本机缺失镜像，并从其他主机流式同步过来。
// 数据经源主机 docker save → SSH → 目标主机 docker load，不在平台落盘。
export default function ImageSyncPanel({ hostId }: { hostId: string }) {
  const [syncOpen, setSyncOpen] = useState(false)
  const [sourceHostId, setSourceHostId] = useState<string | undefined>()

  const hosts = useHosts()
  const precheck = useImagePrecheck(hostId)
  const sync = useImageSync(hostId)

  const results: ImagePrecheckResult[] = precheck.data?.results ?? []
  const missing = results.filter((r) => !r.present)

  const otherHosts = (hosts.data?.hosts ?? []).filter(
    (h) => h.id !== hostId && h.status === 'ok',
  )

  const startSync = () => {
    if (!sourceHostId || missing.length === 0) return
    sync.mutate(
      { source_host_id: sourceHostId, images: missing.map((m) => m.image) },
      {
        onSuccess: (res) => {
          message.success('同步任务已启动')
          setSyncOpen(false)
          window.location.href = `/jobs/${res.job_id}`
        },
        onError: (e) => message.error(`启动失败: ${String(e)}`),
      },
    )
  }

  const columns = [
    {
      title: '镜像',
      dataIndex: 'image',
      key: 'image',
      render: (v: string) => <span className="mono">{v}</span>,
    },
    {
      title: '状态',
      dataIndex: 'present',
      key: 'present',
      width: 120,
      render: (present: boolean) =>
        present ? (
          <Tag color="green">已存在</Tag>
        ) : (
          <Tag color="red">缺失</Tag>
        ),
    },
  ]

  return (
    <Card
      size="small"
      title="镜像管理"
      extra={
        <Space>
          <Button size="small" onClick={() => void precheck.refetch()} loading={precheck.isFetching}>
            重新检查
          </Button>
          <Button
            size="small"
            type="primary"
            icon={<SwapOutlined />}
            disabled={missing.length === 0 || otherHosts.length === 0}
            onClick={() => setSyncOpen(true)}
          >
            从其他主机同步缺失镜像
          </Button>
        </Space>
      }
    >
      {otherHosts.length === 0 && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message="没有其他状态正常的主机可作为镜像来源；先添加并探测一台能拉取镜像的主机。"
        />
      )}
      {missing.length > 0 ? (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={`缺失 ${missing.length} 个镜像，部署会失败`}
          description="可以先把镜像在一台能访问镜像仓库的主机上拉取下来，再用「同步」功能流式传输到本机（docker save | docker load，不占用平台磁盘）。"
        />
      ) : (
        results.length > 0 && (
          <Alert
            type="success"
            showIcon
            style={{ marginBottom: 12 }}
            message="本机所需的全部镜像都已就绪，可以正常部署。"
          />
        )
      )}
      <Table
        size="small"
        rowKey="image"
        columns={columns}
        dataSource={results}
        loading={precheck.isFetching}
        pagination={false}
        locale={{ emptyText: '该主机还没有实例，或尚未探测。' }}
      />

      <Modal
        open={syncOpen}
        title="从其他主机同步镜像"
        onCancel={() => setSyncOpen(false)}
        onOk={startSync}
        okText="开始同步"
        okButtonProps={{ loading: sync.isPending }}
      >
        <Typography.Paragraph type="secondary">
          将以下 {missing.length} 个镜像从源主机流式同步到本机（源 docker save → 本机 docker
          load）。镜像较大的情况下可能持续数分钟，可在任务页查看实时日志。
        </Typography.Paragraph>
        <Space direction="vertical" style={{ width: '100%' }}>
          {missing.map((m) => (
            <Tag key={m.image} className="mono">
              {m.image}
            </Tag>
          ))}
          <Select
            style={{ width: '100%' }}
            placeholder="选择源主机（能拉取镜像的一台）"
            value={sourceHostId}
            onChange={setSourceHostId}
            options={otherHosts.map((h) => ({ value: h.id, label: `${h.name} (${h.address})` }))}
          />
        </Space>
      </Modal>
    </Card>
  )
}
