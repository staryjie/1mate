import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  App,
  Button,
  Card,
  Empty,
  Modal,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd'
import { DeleteOutlined, ImportOutlined, PlayCircleOutlined, PlusOutlined, PauseCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { api } from '../api/client'
import { useHosts, useInstances } from '../api/hooks'
import type { Instance } from '../api/types'
import { InstanceStatusTag } from '../components/StatusTags'
import { formatDateTime } from '../utils/format'

export default function InstancesListPage() {
  const navigate = useNavigate()
  const { message } = App.useApp()
  const [selectedId, setSelectedId] = useState<string>('')
  const [destroyLoading, setDestroyLoading] = useState(false)
  const hosts = useHosts()
  const instances = useInstances()

  const hostList = hosts.data?.hosts ?? []
  const instanceList = instances.data?.instances ?? []

  const hostNameById: Record<string, string> = {}
  for (const h of hostList) hostNameById[h.id] = h.name

  const runAction = async (inst: Instance, name: 'start' | 'stop') => {
    try {
      await api.post<{ job_id: string }>(`/api/v1/instances/${inst.id}/actions/${name}`, {})
      message.success(`任务已排队：${name}`)
      void instances.refetch()
    } catch (err) {
      message.error((err as Error).message)
    }
  }

  const doDestroy = async () => {
    if (!selectedId) return
    setDestroyLoading(true)
    try {
      await api.del(`/api/v1/instances/${selectedId}`)
      message.success('已删除实例记录')
      setSelectedId('')
      void instances.refetch()
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setDestroyLoading(false)
    }
  }

  const actionBtns = (inst: Instance) => {
    const isRunning = inst.status === 'running'
    const isDeploying = inst.status === 'deploying'
    return (
      <Space size="small">
        <Button
          size="small"
          icon={<PlayCircleOutlined />}
          onClick={() => void runAction(inst, 'start')}
          disabled={isRunning || isDeploying}
        >
          启动
        </Button>
        <Button
          size="small"
          icon={<PauseCircleOutlined />}
          onClick={() => void runAction(inst, 'stop')}
          disabled={!isRunning || isDeploying}
        >
          停止
        </Button>
        <Button
          size="small"
          danger
          icon={<DeleteOutlined />}
          onClick={() => setSelectedId(inst.id)}
        >
          删除
        </Button>
      </Space>
    )
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <div className="page-header">
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            实例
          </Typography.Title>
          <Typography.Text type="secondary">
            管理所有 TeslaMate 实例，查看状态并执行操作。
          </Typography.Text>
        </div>
        <Space wrap>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/instances/create')}>
            新建实例
          </Button>
          <Button icon={<ImportOutlined />} onClick={() => navigate('/instances/import')}>
            导入现有实例
          </Button>
          <Button icon={<ReloadOutlined />} onClick={() => void instances.refetch()}>
            刷新
          </Button>
        </Space>
      </div>

      {instanceList.length === 0 ? (
        <Card>
          <Empty
            description={
              <Space direction="vertical">
                <span>暂无实例</span>
                <Button type="primary" size="small" onClick={() => navigate('/instances/create')}>
                  创建第一个实例
                </Button>
              </Space>
            }
          />
        </Card>
      ) : (
        <Table<Instance>
          size="small"
          rowKey="id"
          dataSource={instanceList}
          onRow={(record) => ({
            onClick: () => navigate(`/instances/${record.id}`),
            style: { cursor: 'pointer' },
          })}
          columns={[
            {
              title: '名称',
              dataIndex: 'name',
              render: (_, r) => (
                <span className="mono" onClick={() => navigate(`/instances/${r.id}`)} style={{ cursor: 'pointer' }}>
                  {r.name}
                </span>
              ),
            },
            { title: 'Slug', dataIndex: 'slug', render: (v) => <span className="mono">{v}</span> },
            {
              title: '主机',
              render: (r) => {
                const h = hostList.find((x) => x.id === r.host_id)
                return <span className="mono">{h ? h.name : r.host_id}</span>
              },
            },
            { title: '状态', render: (_, r) => <InstanceStatusTag status={r.status} /> },
            {
              title: '端口',
              render: (r) => (
                <span className="mono">
                  {r.tm_port} / {r.grafana_port}{r.disable_mqtt ? '' : ` / ${r.mqtt_port}`}
                </span>
              ),
            },
            {
              title: 'Compose',
              render: (r) => <Tag color={r.compose_major === 2 ? 'green' : 'gold'}>v{r.compose_major}</Tag>,
            },
            {
              title: '更新时间',
              dataIndex: 'updated_at',
              render: (v) => formatDateTime(v),
            },
            {
              title: '操作',
              render: (_, r) => actionBtns(r),
            },
          ]}
        />
      )}

      {/* Delete confirmation modal */}
      <Modal
        open={Boolean(selectedId)}
        title="删除实例记录"
        onCancel={() => setSelectedId('')}
        onOk={doDestroy}
        okText="确认删除"
        okButtonProps={{ danger: true, loading: destroyLoading }}
      >
        <Typography.Text type="warning">
          确定要删除选中的实例记录吗？远程容器与数据卷不会被自动删除。
        </Typography.Text>
      </Modal>
    </Space>
  )
}
