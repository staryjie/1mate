import { useMemo, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Input,
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
// 也支持手动输入任意镜像引用（例如给还没有实例的新主机预同步镜像）。
export default function ImageSyncPanel({ hostId }: { hostId: string }) {
  const [syncOpen, setSyncOpen] = useState(false)
  const [sourceHostId, setSourceHostId] = useState<string | undefined>()
  const [manualImages, setManualImages] = useState('')
  const [syncedImages, setSyncedImages] = useState<string[]>([])

  const hosts = useHosts()
  const precheck = useImagePrecheck(hostId, syncedImages)
  const sync = useImageSync(hostId)

  const results: ImagePrecheckResult[] = precheck.data?.results ?? []
  const missing = results.filter((r) => !r.present)

  // 合并 syncedImages 中不在 results 中的镜像（标记为 present，因刚刚同步成功）
  const mergedResults = useMemo(() => {
    const existing = new Set(results.map((r) => r.image))
    const extra = syncedImages
      .filter((img) => !existing.has(img))
      .map((img) => ({ image: img, present: true }))
    return [...results, ...extra]
  }, [results, syncedImages])

  const otherHosts = (hosts.data?.hosts ?? []).filter(
    (h) => h.id !== hostId && h.status === 'ok',
  )

  // 手动输入的镜像引用：按行/空格切分，去重、去空。
  const manualList = useMemo(
    () =>
      Array.from(
        new Set(
          manualImages
            .split(/[\s,;]+/)
            .map((s) => s.trim())
            .filter(Boolean),
        ),
      ),
    [manualImages],
  )

  // 同步清单 = 预检缺失的镜像 + 手动输入的镜像（去重）。
  const syncList = useMemo(() => {
    const set = new Set(missing.map((m) => m.image))
    for (const img of manualList) set.add(img)
    return Array.from(set)
  }, [missing, manualList])

  const startSync = () => {
    if (!sourceHostId || syncList.length === 0) return
    sync.mutate(
      { source_host_id: sourceHostId, images: syncList },
      {
        onSuccess: () => {
          message.success('同步任务已启动')
          setSyncOpen(false)
          setManualImages('')
          // 将同步的镜像加入待检查列表，使面板立即显示同步结果
          setSyncedImages((prev) => {
            const set = new Set(prev)
            for (const img of syncList) set.add(img)
            return Array.from(set)
          })
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
            disabled={otherHosts.length === 0}
            onClick={() => setSyncOpen(true)}
          >
            从其他主机同步镜像
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
        dataSource={mergedResults}
        loading={precheck.isFetching}
        pagination={false}
        locale={{
          emptyText: '该主机还没有实例，或尚未探测。可在下方同步对话框中手动输入镜像引用，预先同步。',
        }}
      />

      <Modal
        open={syncOpen}
        title="从其他主机同步镜像"
        onCancel={() => setSyncOpen(false)}
        onOk={startSync}
        okText="开始同步"
        okButtonProps={{ loading: sync.isPending, disabled: syncList.length === 0 || !sourceHostId }}
      >
        <Typography.Paragraph type="secondary">
          镜像从源主机流式同步到本机（源 docker save → 本机 docker load）。镜像较大时可能持续数分钟，可在任务页查看实时日志。
        </Typography.Paragraph>
        <Space direction="vertical" style={{ width: '100%' }} size="middle">
          {missing.length > 0 && (
            <div>
              <Typography.Paragraph style={{ marginBottom: 4 }}>
                预检缺失的镜像（自动带入）：
              </Typography.Paragraph>
              <Space wrap>
                {missing.map((m) => (
                  <Tag key={m.image} className="mono">
                    {m.image}
                  </Tag>
                ))}
              </Space>
            </div>
          )}
          <div>
            <Typography.Paragraph style={{ marginBottom: 4 }}>
              手动添加镜像（每行一个，可选。例如给新主机预先同步 teslamate/teslamate:latest）：
            </Typography.Paragraph>
            <Input.TextArea
              rows={3}
              value={manualImages}
              onChange={(e) => setManualImages(e.target.value)}
              placeholder={'teslamate/teslamate:latest\npostgres:18-trixie\nteslamate/grafana:latest'}
            />
          </div>
          <Select
            style={{ width: '100%' }}
            placeholder="选择源主机（能拉取镜像的一台）"
            value={sourceHostId}
            onChange={setSourceHostId}
            options={otherHosts.map((h) => ({ value: h.id, label: `${h.name} (${h.address})` }))}
          />
          {syncList.length > 0 && (
            <div>
              <Typography.Paragraph style={{ marginBottom: 4 }}>
                共 {syncList.length} 个镜像将同步：
              </Typography.Paragraph>
              <Space wrap>
                {syncList.map((img) => (
                  <Tag key={img} className="mono" color="blue">
                    {img}
                  </Tag>
                ))}
              </Space>
            </div>
          )}
        </Space>
      </Modal>
    </Card>
  )
}
