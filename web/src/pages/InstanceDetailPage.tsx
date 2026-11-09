import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Descriptions,
  Divider,
  Form,
  Input,
  InputNumber,
  Modal,
  Space,
  Spin,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
  Upload,
} from 'antd'
import {
  CloudDownloadOutlined,
  DeleteOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  RocketOutlined,
  SyncOutlined,
  UploadOutlined,
  ArrowUpOutlined,
} from '@ant-design/icons'
import {
  useBackups,
  useComposePreview,
  useInstance,
  useInstanceAction,
  useJobs,
  useUpdateInstance,
  useUploadBackup,
} from '../api/hooks'
import { api } from '../api/client'
import type { Backup } from '../api/types'
import { InstanceStatusTag, JobStatusTag, jobKindLabel } from '../components/StatusTags'
import LogViewer from '../components/LogViewer'
import RevealSecret from '../components/RevealSecret'
import { formatBytes, formatDateTime } from '../utils/format'

export default function InstanceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const { message, modal } = App.useApp()
  const instance = useInstance(id)
  const jobs = useJobs({ instance_id: id })
  const backups = useBackups(id)
  const preview = useComposePreview(id)
  const action = useInstanceAction(id ?? '')
  const update = useUpdateInstance(id ?? '')
  const upload = useUploadBackup(id ?? '')

  const [editOpen, setEditOpen] = useState(false)
  const [restoreOpen, setRestoreOpen] = useState(false)
  const [destroyOpen, setDestroyOpen] = useState(false)
  const [destroySlug, setDestroySlug] = useState('')
  const [purgeVolumes, setPurgeVolumes] = useState(false)
  const [selectedBackup, setSelectedBackup] = useState<string>('')
  const [logsOpen, setLogsOpen] = useState(false)
  const [logs, setLogs] = useState('')
  const [logsLoading, setLogsLoading] = useState(false)
  const [editForm] = Form.useForm()
  const inst = instance.data

  const run = async (name: Parameters<typeof action.mutateAsync>[0]['action'], body?: Record<string, unknown>) => {
    try {
      const res = await action.mutateAsync({ action: name, body })
      message.success('任务已排队')
      void instance.refetch()
      void jobs.refetch()
      return res
    } catch (err) {
      message.error((err as Error).message)
      return null
    }
  }

  useEffect(() => {
    if (editOpen && inst) {
      editForm.setFieldsValue({
        name: inst.name,
        bind_address: inst.bind_address,
        tm_port: inst.tm_port,
        grafana_port: inst.grafana_port,
        mqtt_port: inst.mqtt_port,
        timezone: inst.timezone,
        region: inst.region,
        image_tag: inst.image_tag,
        postgres_tag: inst.postgres_tag,
        grafana_admin_user: inst.grafana_admin_user,
        disable_mqtt: inst.disable_mqtt,
      })
    }
  }, [editOpen, inst, editForm])

  if (instance.isLoading) {
    return (
      <div style={{ padding: 80, textAlign: 'center' }}>
        <Spin size="large" />
      </div>
    )
  }
  if (!inst) {
    return <Alert type="error" showIcon message="实例不存在" />
  }

  const busy = action.isPending
  const activeJob = jobs.data?.jobs.find((j) => j.status === 'running' || j.status === 'queued')
  const latestJob = jobs.data?.jobs[0]

  const openLogs = async () => {
    setLogsOpen(true)
    setLogsLoading(true)
    try {
      const res = await api.get<{ logs: string; exit_code: number }>(`/api/v1/instances/${inst.id}/logs?tail=200`)
      setLogs(res.logs)
    } catch (err) {
      setLogs((err as Error).message)
    } finally {
      setLogsLoading(false)
    }
  }

  const doRestore = async () => {
    if (!selectedBackup) {
      message.warning('请选择要恢复的备份')
      return
    }
    const res = await run('restore', { backup_id: selectedBackup })
    if (res) setRestoreOpen(false)
  }

  const doDestroy = async () => {
    if (purgeVolumes && destroySlug !== inst.slug) {
      message.error('请输入正确的 slug 以确认删除数据')
      return
    }
    const res = await run('destroy', { purge_volumes: purgeVolumes, confirm_slug: destroySlug })
    if (res) {
      setDestroyOpen(false)
      setDestroySlug('')
      setPurgeVolumes(false)
    }
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <div className="page-header">
        <div>
          <Space align="center" wrap>
            <Typography.Title level={3} style={{ margin: 0 }}>
              {inst.name}
            </Typography.Title>
            <InstanceStatusTag status={inst.status} />
            <Tag color={inst.compose_major === 2 ? 'green' : 'gold'}>Compose v{inst.compose_major}</Tag>
          </Space>
          <div className="muted mono">
            {inst.project_name} · {inst.base_dir}
          </div>
        </div>
        <Space wrap>
          <Button icon={<ReloadOutlined />} onClick={() => void instance.refetch()}>
            刷新
          </Button>
          <Button icon={<SyncOutlined />} onClick={() => void run('reconcile')} loading={busy}>
            对账
          </Button>
          <Button icon={<PlayCircleOutlined />} onClick={() => void run('start')} loading={busy}>
            启动
          </Button>
          <Button icon={<PauseCircleOutlined />} onClick={() => void run('stop')} loading={busy}>
            停止
          </Button>
          <Button
            icon={<RocketOutlined />}
            type="primary"
            onClick={() => void run('deploy')}
            loading={busy || inst.status === 'deploying'}
          >
            部署 / 重新部署
          </Button>
        </Space>
      </div>

      {inst.status_message && (
        <Alert
          type={inst.status === 'failed' ? 'error' : 'warning'}
          showIcon
          message={inst.status_message}
          action={
            latestJob ? (
              <Button size="small" onClick={() => navigate(`/jobs/${latestJob.id}`)}>
                查看任务
              </Button>
            ) : undefined
          }
        />
      )}

      {inst.status === 'created' && (
        <Alert
          type="info"
          showIcon
          message="该实例尚未部署"
          description="点击“部署 / 重新部署”会在目标主机上生成 compose 配置并启动容器。"
          action={
            <Button size="small" type="primary" onClick={() => void run('deploy')}>
              立即部署
            </Button>
          }
        />
      )}

      {activeJob && (
        <Card size="small" title={`进行中任务：${jobKindLabel(activeJob.kind)}`}>
          <LogViewer jobId={activeJob.id} height={240} onDone={() => void instance.refetch()} />
        </Card>
      )}

      <Tabs
        defaultActiveKey="overview"
        items={[
          {
            key: 'overview',
            label: '概览',
            children: (
              <Space direction="vertical" size="middle" style={{ width: '100%' }}>
                <Card size="small">
                  <Descriptions bordered size="small" column={{ xs: 1, md: 2 }}>
                    <Descriptions.Item label="TeslaMate">
                      <span className="mono">{inst.tm_port}</span>
                      <CopyLinkButton url={`http://${inst.bind_address === '0.0.0.0' ? '主机IP' : inst.bind_address}:${inst.tm_port}`} />
                    </Descriptions.Item>
                    <Descriptions.Item label="Grafana">
                      <span className="mono">{inst.grafana_port}</span>
                    </Descriptions.Item>
                    <Descriptions.Item label="MQTT">
                      {inst.disable_mqtt ? <Tag>已关闭</Tag> : <span className="mono">{inst.mqtt_port}</span>}
                    </Descriptions.Item>
                    <Descriptions.Item label="时区">{inst.timezone}</Descriptions.Item>
                    <Descriptions.Item label="Tesla 区域">{inst.region === 'cn' ? '中国' : '国际'}</Descriptions.Item>
                    <Descriptions.Item label="镜像标签">{inst.image_tag}</Descriptions.Item>
                    <Descriptions.Item label="PostgreSQL">{inst.postgres_tag}</Descriptions.Item>
                    <Descriptions.Item label="Grafana 管理员">{inst.grafana_admin_user}</Descriptions.Item>
                    <Descriptions.Item label="绑定地址">{inst.bind_address}</Descriptions.Item>
                    <Descriptions.Item label="数据库用户">{inst.db_user}</Descriptions.Item>
                    <Descriptions.Item label="创建时间">{formatDateTime(inst.created_at)}</Descriptions.Item>
                    <Descriptions.Item label="更新时间">{formatDateTime(inst.updated_at)}</Descriptions.Item>
                  </Descriptions>
                  <Divider />
                  <Space wrap>
                    <RevealSecret
                      title="TeslaMate 加密密钥"
                      buttonText="查看加密密钥"
                      description="恢复备份到新实例时必须使用相同的 ENCRYPTION_KEY，否则已保存的 Tesla 令牌无法解密。"
                      onReveal={async () => {
                        const res = await api.post<{ tm_encryption_key: string }>(
                          `/api/v1/instances/${inst.id}/actions/reveal-key`,
                        )
                        return res.tm_encryption_key
                      }}
                    />
                    <Button onClick={() => setEditOpen(true)}>修改配置</Button>
                    <Button icon={<ArrowUpOutlined />} onClick={() => void run('upgrade')} loading={busy}>
                      备份并升级镜像
                    </Button>
                    <Button onClick={() => void openLogs()}>查看容器日志</Button>
                  </Space>
                </Card>

                <Card size="small" title="访问提示">
                  <Typography.Paragraph style={{ marginBottom: 4 }}>
                    首次部署后，请在 TeslaMate 界面使用 Tesla 账户登录以生成令牌。TeslaMate 自身不保存 Tesla 密码。
                  </Typography.Paragraph>
                  <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
                    公网暴露前请配置 TLS 反向代理与访问控制，参考部署文档中的 Traefik 示例。
                  </Typography.Paragraph>
                </Card>
              </Space>
            ),
          },
          {
            key: 'compose',
            label: '配置预览',
            children: (
              <Space direction="vertical" style={{ width: '100%' }}>
                <Space wrap>
                  <Tag>项目：{preview.data?.project_name ?? inst.project_name}</Tag>
                  <Tag>目录：{preview.data?.base_dir ?? inst.base_dir}</Tag>
                  {preview.data && (
                    <Tag color="blue">
                      命令：{preview.data.command_prefix} -p {preview.data.project_name}
                    </Tag>
                  )}
                </Space>
                {preview.isLoading && <Spin />}
                {preview.data && (
                  <>
                    <Card size="small" title="compose.yaml">
                      <pre className="code-block">{preview.data.compose}</pre>
                    </Card>
                    <Card size="small" title=".env（已脱敏）">
                      <pre className="code-block">{preview.data.env}</pre>
                    </Card>
                  </>
                )}
              </Space>
            ),
          },
          {
            key: 'backups',
            label: `备份 (${backups.data?.backups.length ?? 0})`,
            children: (
              <Space direction="vertical" style={{ width: '100%' }}>
                <Space wrap>
                  <Button onClick={() => void run('backup')} loading={busy}>
                    立即备份
                  </Button>
                  <Upload
                    showUploadList={false}
                    customRequest={async ({ file, onSuccess, onError }) => {
                      try {
                        await upload.mutateAsync({ file: file as File })
                        message.success('备份文件已上传')
                        onSuccess?.(null)
                      } catch (err) {
                        message.error((err as Error).message)
                        onError?.(err as Error)
                      }
                    }}
                  >
                    <Button icon={<UploadOutlined />} loading={upload.isPending}>
                      上传备份
                    </Button>
                  </Upload>
                  <Button onClick={() => setRestoreOpen(true)}>从备份恢复</Button>
                </Space>
                <Table<Backup>
                  size="small"
                  rowKey="id"
                  loading={backups.isLoading}
                  dataSource={backups.data?.backups ?? []}
                  columns={[
                    { title: '文件', dataIndex: 'filename', render: (v) => <span className="mono">{v}</span> },
                    { title: '大小', render: (_, r) => formatBytes(r.size_bytes) },
                    { title: '说明', dataIndex: 'note' },
                    { title: '时间', render: (_, r) => formatDateTime(r.created_at) },
                    {
                      title: '操作',
                      render: (_, r) => (
                        <Space>
                          <Tooltip title="下载">
                            <Button
                              size="small"
                              icon={<CloudDownloadOutlined />}
                              onClick={() => {
                                window.location.href = `/api/v1/backups/${r.id}/download`
                              }}
                            />
                          </Tooltip>
                          <Button
                            size="small"
                            onClick={() => {
                              setSelectedBackup(r.id)
                              setRestoreOpen(true)
                            }}
                          >
                            恢复
                          </Button>
                          <Button
                            size="small"
                            danger
                            icon={<DeleteOutlined />}
                            onClick={() => {
                              modal.confirm({
                                title: '删除该备份文件？',
                                content: r.filename,
                                okButtonProps: { danger: true },
                                onOk: async () => {
                                  try {
                                    await api.del(`/api/v1/backups/${r.id}`)
                                    void backups.refetch()
                                  } catch (err) {
                                    message.error((err as Error).message)
                                  }
                                },
                              })
                            }}
                          />
                        </Space>
                      ),
                    },
                  ]}
                />
              </Space>
            ),
          },
          {
            key: 'jobs',
            label: `任务 (${jobs.data?.jobs.length ?? 0})`,
            children: (
              <Table
                size="small"
                rowKey="id"
                dataSource={jobs.data?.jobs ?? []}
                onRow={(record) => ({
                  onClick: () => navigate(`/jobs/${record.id}`),
                  style: { cursor: 'pointer' },
                })}
                columns={[
                  { title: '类型', dataIndex: 'kind', render: (v: string) => jobKindLabel(v) },
                  { title: '状态', render: (_, r) => <JobStatusTag status={r.status} /> },
                  { title: '阶段', dataIndex: 'stage', render: (v) => v || '—' },
                  { title: '创建时间', render: (_, r) => formatDateTime(r.created_at) },
                ]}
              />
            ),
          },
          {
            key: 'danger',
            label: '危险区',
            children: (
              <Card size="small" style={{ borderColor: '#ffccc7' }}>
                <Space direction="vertical" style={{ width: '100%' }}>
                  <Typography.Text strong>销毁实例</Typography.Text>
                  <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
                    默认只停止并移除容器与网络，数据卷会保留。勾选删除数据会连同 PostgreSQL 与 Grafana 卷一并移除，
                    且不可恢复。
                  </Typography.Paragraph>
                  <Button danger onClick={() => setDestroyOpen(true)}>
                    销毁实例
                  </Button>
                  <Divider />
                  <Typography.Text strong>从平台移除记录</Typography.Text>
                  <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
                    仅删除平台数据库中的记录，不会改动远程主机。适用于主机已经消失的情况；备份文件会一并删除。
                  </Typography.Paragraph>
                  <Button danger icon={<DeleteOutlined />} onClick={() => {
                    modal.confirm({
                      title: `从平台移除 ${inst.name}？`,
                      content: '远程容器与数据卷不会被删除。',
                      okButtonProps: { danger: true },
                      onOk: async () => {
                        try {
                          await api.del(`/api/v1/instances/${inst.id}`)
                          message.success('已移除记录')
                          navigate('/')
                        } catch (err) {
                          message.error((err as Error).message)
                        }
                      },
                    })
                  }}>
                    移除记录
                  </Button>
                </Space>
              </Card>
            ),
          },
        ]}
      />

      {/* Edit configuration */}
      <Modal
        open={editOpen}
        title="修改实例配置"
        onCancel={() => setEditOpen(false)}
        onOk={async () => {
          try {
            const values = await editForm.validateFields()
            await update.mutateAsync(values)
            message.success('配置已保存；重新部署后生效')
            setEditOpen(false)
          } catch (err) {
            message.error((err as Error).message)
          }
        }}
        okText="保存"
        confirmLoading={update.isPending}
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="修改端口等配置后需要重新部署才会生效"
        />
        <Form form={editForm} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Space style={{ display: 'flex' }} align="start">
            <Form.Item name="tm_port" label="TeslaMate 端口" rules={[{ required: true }]} style={{ flex: 1 }}>
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="grafana_port" label="Grafana 端口" rules={[{ required: true }]} style={{ flex: 1 }}>
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="mqtt_port" label="MQTT 端口" rules={[{ required: true }]} style={{ flex: 1 }}>
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
          </Space>
          <Form.Item name="bind_address" label="绑定地址">
            <Input />
          </Form.Item>
          <Space style={{ display: 'flex' }} align="start">
            <Form.Item name="image_tag" label="TeslaMate 镜像" style={{ flex: 1 }}>
              <Input />
            </Form.Item>
            <Form.Item name="postgres_tag" label="PostgreSQL 镜像" style={{ flex: 1 }}>
              <Input />
            </Form.Item>
          </Space>
          <Form.Item name="timezone" label="时区">
            <Input />
          </Form.Item>
          <Form.Item name="region" label="Tesla 区域">
            <Input />
          </Form.Item>
          <Form.Item name="grafana_admin_user" label="Grafana 管理员">
            <Input />
          </Form.Item>
          <Form.Item name="disable_mqtt" label="关闭 MQTT" valuePropName="checked">
            <Checkbox />
          </Form.Item>
        </Form>
      </Modal>

      {/* Restore */}
      <Modal
        open={restoreOpen}
        title="从备份恢复"
        onCancel={() => setRestoreOpen(false)}
        onOk={() => void doRestore()}
        okText="开始恢复"
        okButtonProps={{ danger: true, loading: busy }}
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Alert
            type="warning"
            showIcon
            message="恢复会覆盖当前数据库"
            description="任务执行前会自动创建一次安全备份，随后停止 TeslaMate、恢复数据并重新启动。"
          />
          <Typography.Text>选择备份文件</Typography.Text>
          <Space direction="vertical" style={{ width: '100%' }}>
            {(backups.data?.backups ?? []).map((b) => (
              <Card
                key={b.id}
                size="small"
                hoverable
                onClick={() => setSelectedBackup(b.id)}
                style={{
                  borderColor: selectedBackup === b.id ? '#1677ff' : undefined,
                  cursor: 'pointer',
                }}
              >
                <Space wrap>
                  <span className="mono">{b.filename}</span>
                  <Tag>{formatBytes(b.size_bytes)}</Tag>
                  <Typography.Text type="secondary">{formatDateTime(b.created_at)}</Typography.Text>
                  {b.note && <Tag color="blue">{b.note}</Tag>}
                </Space>
              </Card>
            ))}
            {(backups.data?.backups ?? []).length === 0 && (
              <Typography.Text type="secondary">暂无备份，请先创建或上传备份文件。</Typography.Text>
            )}
          </Space>
        </Space>
      </Modal>

      {/* Destroy */}
      <Modal
        open={destroyOpen}
        title="销毁实例"
        onCancel={() => setDestroyOpen(false)}
        onOk={() => void doDestroy()}
        okText="执行销毁"
        okButtonProps={{ danger: true, loading: busy }}
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Alert
            type="error"
            showIcon
            message="该操作会停止并移除容器"
            description="默认保留数据卷。勾选删除数据会永久移除数据库与 Grafana 数据。"
          />
          <Checkbox checked={purgeVolumes} onChange={(e) => setPurgeVolumes(e.target.checked)}>
            同时删除数据卷与实例目录（不可恢复）
          </Checkbox>
          {purgeVolumes && (
            <Space direction="vertical" style={{ width: '100%' }}>
              <Typography.Text>
                请输入 slug <span className="inline-code">{inst.slug}</span> 以确认：
              </Typography.Text>
              <Input
                value={destroySlug}
                onChange={(e) => setDestroySlug(e.target.value)}
                placeholder={inst.slug}
              />
            </Space>
          )}
          <Typography.Text type="secondary">
            建议先创建备份：<Button size="small" onClick={() => void run('backup')}>立即备份</Button>
          </Typography.Text>
        </Space>
      </Modal>

      {/* Container logs */}
      <Modal open={logsOpen} title="容器日志（最近 200 行）" width={900} footer={null} onCancel={() => setLogsOpen(false)}>
        {logsLoading ? <Spin /> : <pre className="code-block">{logs || '暂无日志'}</pre>}
        <Space style={{ marginTop: 12 }}>
          <Button icon={<ReloadOutlined />} onClick={() => void openLogs()}>
            重新获取
          </Button>
        </Space>
      </Modal>
    </Space>
  )
}

function CopyLinkButton({ url }: { url: string }) {
  const { message } = App.useApp()
  return (
    <Tooltip title="复制访问地址">
      <Button
        type="link"
        size="small"
        onClick={() => {
          void navigator.clipboard.writeText(url).then(
            () => message.success('已复制'),
            () => message.warning('复制失败，请手动复制'),
          )
        }}
      >
        {url}
      </Button>
    </Tooltip>
  )
}
