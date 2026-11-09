import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Descriptions,
  Form,
  Input,
  Modal,
  Radio,
  Select,
  Space,
  Spin,
  Steps,
  Tabs,
  Tag,
  Typography,
} from 'antd'
import { DownloadOutlined, PlayCircleOutlined, ReloadOutlined, ToolOutlined } from '@ant-design/icons'
import {
  useHost,
  useHostAction,
  useInstallDocker,
  useInstallScript,
  useInstances,
  type InstallRequest,
} from '../api/hooks'
import { ComposeKindTag, HostStatusTag, InstanceStatusTag } from '../components/StatusTags'
import CapabilityPanel from '../components/CapabilityPanel'
import LogViewer from '../components/LogViewer'
import { formatDateTime } from '../utils/format'

type Strategy = 'repo' | 'compose_only' | 'script'

interface InstallFormValues {
  strategy: Strategy
  docker_version?: string
  mirror: string
  compose_version?: string
  registry_mirrors?: string
  save_sudo_password?: string
  accept_system_changes: boolean
}

export default function HostDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const { message } = App.useApp()
  const hostQuery = useHost(id)
  const instances = useInstances(id)
  const probe = useHostAction()
  const install = useInstallDocker(id ?? '')

  const [installOpen, setInstallOpen] = useState(false)
  const [stage, setStage] = useState<'form' | 'preview' | 'running'>('form')
  const [form] = Form.useForm<InstallFormValues>()
  const [previewParams, setPreviewParams] = useState<Record<string, string>>({})
  const [jobId, setJobId] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const strategy = Form.useWatch('strategy', form)
  const mirror = Form.useWatch('mirror', form)

  const preview = useInstallScript(id ?? '', previewParams)
  const host = hostQuery.data?.host
  const caps = hostQuery.data?.capabilities

  useEffect(() => {
    if (!installOpen) return
    setStage('form')
    setJobId('')
    form.setFieldsValue({
      strategy: 'repo',
      mirror: 'none',
      accept_system_changes: false,
    })
  }, [installOpen, form])

  if (hostQuery.isLoading) {
    return (
      <div style={{ padding: 80, textAlign: 'center' }}>
        <Spin size="large" />
      </div>
    )
  }
  if (!host) {
    return <Alert type="error" showIcon message="主机不存在或已被删除" />
  }

  const openInstall = (preset: Strategy) => {
    setInstallOpen(true)
    form.setFieldsValue({
      strategy: preset,
      mirror: 'none',
      accept_system_changes: false,
    })
  }

  const goPreview = async () => {
    try {
      const values = await form.validateFields()
      setPreviewParams({
        strategy: values.strategy,
        mirror: values.mirror === 'none' ? '' : values.mirror,
        docker_version: values.docker_version ?? '',
        compose_version: values.compose_version ?? '',
      })
      setStage('preview')
    } catch {
      /* validation errors are shown inline */
    }
  }

  const startInstall = async () => {
    const values = form.getFieldsValue(true) as InstallFormValues
    if (!values.accept_system_changes) {
      message.warning('请先勾选确认，安装会修改远程主机')
      return
    }
    const payload: InstallRequest = {
      strategy: values.strategy,
      mirror: values.mirror === 'none' ? '' : values.mirror,
      accept_system_changes: true,
    }
    if (values.docker_version) payload.docker_version = values.docker_version
    if (values.compose_version) payload.compose_version = values.compose_version
    if (values.save_sudo_password) payload.save_sudo_password = values.save_sudo_password
    if (values.registry_mirrors) {
      const mirrors = values.registry_mirrors
        .split(/[\s,]+/)
        .map((s) => s.trim())
        .filter(Boolean)
      if (mirrors.length > 0) payload.registry_mirrors = mirrors
    }
    setSubmitting(true)
    try {
      const res = await install.mutateAsync(payload)
      setJobId(res.job_id)
      setStage('running')
      message.success('安装任务已开始')
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  const needsComposeV2 = caps?.compose_major === 1 || caps?.compose_kind === 'standalone_v1'
  const noCompose = caps && caps.compose_kind === 'none'
  const noDocker = !caps?.docker_present

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <div className="page-header">
        <div>
          <Space align="center">
            <Typography.Title level={3} style={{ margin: 0 }}>
              {host.name}
            </Typography.Title>
            <HostStatusTag status={host.status} />
          </Space>
          <div className="muted mono">
            {host.ssh_user}@{host.address}:{host.ssh_port}
          </div>
        </div>
        <Space wrap>
          <Button
            icon={<ReloadOutlined />}
            loading={probe.isPending}
            onClick={() => probe.mutate({ id: host.id, action: 'probe' })}
          >
            重新探测
          </Button>
          {needsComposeV2 && (
            <Button icon={<ToolOutlined />} onClick={() => openInstall('compose_only')}>
              安装 Compose v2
            </Button>
          )}
          <Button type="primary" icon={<ToolOutlined />} onClick={() => openInstall('repo')}>
            {noDocker ? '一键安装 Docker + Compose' : noCompose ? '安装 Docker Compose' : '重新安装/升级'}
          </Button>
          <Button icon={<PlayCircleOutlined />} onClick={() => navigate(`/instances/new?host=${host.id}`)}>
            新建实例
          </Button>
        </Space>
      </div>

      {caps?.compose_major === 1 && (
        <Alert
          type="warning"
          showIcon
          message="该主机只有 Docker Compose v1"
          description={
            <Space direction="vertical">
              <span>
                v1 使用 <span className="inline-code">docker-compose</span> 命令且已停止维护。平台仍可部署，但建议先安装 v2 插件，
                之后平台会自动切换到 <span className="inline-code">docker compose</span>。
              </span>
              <Button size="small" type="primary" onClick={() => openInstall('compose_only')}>
                立即安装 Compose v2
              </Button>
            </Space>
          }
        />
      )}
      {noDocker && (
        <Alert
          type="warning"
          showIcon
          message="该主机尚未安装 Docker"
          description="可以一键安装 Docker Engine 与 Compose v2，安装完成会自动重新探测。"
          action={
            <Button size="small" type="primary" onClick={() => openInstall('repo')}>
              一键安装
            </Button>
          }
        />
      )}
      {host.status === 'host_key_changed' && (
        <Alert
          type="error"
          showIcon
          message="SSH 主机密钥已变更"
          description="这可能是主机重装，也可能是中间人攻击。确认无误后请在编辑主机时选择信任新指纹。"
        />
      )}
      {host.status === 'docker_socket_denied' && (
        <Alert
          type="warning"
          showIcon
          message="SSH 用户无权访问 Docker socket"
          description="请在编辑主机时配置 sudo（nopasswd 或密码），平台会用 sudo 前缀执行 docker 命令。"
        />
      )}

      <Tabs
        defaultActiveKey="environment"
        items={[
          {
            key: 'environment',
            label: '环境面板',
            children: (
              <CapabilityPanel
                host={host}
                capabilities={caps}
                probing={probe.isPending || host.status === 'installing'}
                onProbe={() => probe.mutate({ id: host.id, action: 'probe' })}
              />
            ),
          },
          {
            key: 'instances',
            label: `实例 (${instances.data?.instances.length ?? 0})`,
            children: (
              <Space direction="vertical" style={{ width: '100%' }}>
                {instances.data?.instances.length ? (
                  instances.data.instances.map((inst) => (
                    <Card
                      key={inst.id}
                      size="small"
                      hoverable
                      onClick={() => navigate(`/instances/${inst.id}`)}
                      title={
                        <Space>
                          {inst.name}
                          <InstanceStatusTag status={inst.status} />
                        </Space>
                      }
                      extra={<span className="mono">:{inst.tm_port}</span>}
                    >
                      <Space wrap>
                        <Tag>Grafana :{inst.grafana_port}</Tag>
                        {!inst.disable_mqtt && <Tag>MQTT :{inst.mqtt_port}</Tag>}
                        <Tag color={inst.compose_major === 2 ? 'green' : 'gold'}>Compose v{inst.compose_major}</Tag>
                        <Typography.Text type="secondary">{inst.base_dir}</Typography.Text>
                      </Space>
                    </Card>
                  ))
                ) : (
                  <Alert
                    type="info"
                    showIcon
                    message="该主机还没有实例"
                    action={
                      <Button size="small" type="primary" onClick={() => navigate(`/instances/new?host=${host.id}`)}>
                        新建实例
                      </Button>
                    }
                  />
                )}
              </Space>
            ),
          },
          {
            key: 'info',
            label: '连接信息',
            children: (
              <Descriptions bordered size="small" column={{ xs: 1, md: 2 }}>
                <Descriptions.Item label="名称">{host.name}</Descriptions.Item>
                <Descriptions.Item label="地址">
                  <span className="mono">{host.address}</span>
                </Descriptions.Item>
                <Descriptions.Item label="SSH 端口">{host.ssh_port}</Descriptions.Item>
                <Descriptions.Item label="SSH 用户">{host.ssh_user}</Descriptions.Item>
                <Descriptions.Item label="认证方式">{host.auth_type === 'key' ? '私钥' : '密码'}</Descriptions.Item>
                <Descriptions.Item label="sudo 方式">{host.sudo_mode}</Descriptions.Item>
                <Descriptions.Item label="主机指纹" span={2}>
                  <span className="mono">{host.host_key_fingerprint || '尚未固定'}</span>
                </Descriptions.Item>
                <Descriptions.Item label="最近探测">{formatDateTime(host.last_probe_at)}</Descriptions.Item>
                <Descriptions.Item label="状态说明">{host.status_message || '—'}</Descriptions.Item>
              </Descriptions>
            ),
          },
        ]}
      />

      <Modal
        open={installOpen}
        title="安装 Docker / Docker Compose"
        width={780}
        onCancel={() => setInstallOpen(false)}
        maskClosable={false}
        footer={
          stage === 'form' ? (
            <Space>
              <Button onClick={() => setInstallOpen(false)}>取消</Button>
              <Button type="primary" onClick={() => void goPreview()}>
                下一步：查看安装脚本
              </Button>
            </Space>
          ) : stage === 'preview' ? (
            <Space>
              <Button onClick={() => setStage('form')}>上一步</Button>
              <Button type="primary" danger loading={submitting} onClick={() => void startInstall()}>
                确认并执行安装
              </Button>
            </Space>
          ) : (
            <Button
              type="primary"
              onClick={() => {
                setInstallOpen(false)
                void hostQuery.refetch()
              }}
            >
              完成
            </Button>
          )
        }
      >
        <Steps
          size="small"
          current={stage === 'form' ? 0 : stage === 'preview' ? 1 : 2}
          items={[{ title: '选择方式' }, { title: '确认脚本' }, { title: '执行安装' }]}
          style={{ marginBottom: 20 }}
        />

        {stage === 'form' && (
          <Form form={form} layout="vertical" requiredMark={false}>
            <Form.Item name="strategy" label="安装策略">
              <Radio.Group>
                <Radio.Button value="repo">官方仓库（推荐）</Radio.Button>
                <Radio.Button value="compose_only">仅安装 Compose v2</Radio.Button>
                <Radio.Button value="script">官方脚本</Radio.Button>
              </Radio.Group>
            </Form.Item>
            <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
              {strategy === 'repo' && '从发行版官方仓库安装 docker-ce、containerd 与 docker-compose-plugin（即 v2）。'}
              {strategy === 'compose_only' && '仅补齐 Compose v2，不触碰已安装的 Docker Engine。'}
              {strategy === 'script' && '使用 get.docker.com 脚本，执行前会先 --dry-run 预览。'}
            </Typography.Paragraph>

            <Form.Item name="mirror" label="软件源镜像">
              <Select
                options={[
                  { value: 'none', label: '官方源（download.docker.com）' },
                  { value: 'Aliyun', label: '阿里云镜像' },
                  { value: 'AzureChinaCloud', label: 'Azure 中国镜像' },
                ]}
              />
            </Form.Item>
            {mirror && mirror !== 'none' && (
              <Typography.Paragraph type="secondary" style={{ marginTop: -8 }}>
                仅当主机访问官方源较慢时使用。镜像源由 Docker 官方安装脚本支持。
              </Typography.Paragraph>
            )}

            <Space style={{ display: 'flex' }} align="start">
              <Form.Item name="docker_version" label="Docker 版本（可选）" style={{ flex: 1 }}>
                <Input placeholder="留空安装最新稳定版，例如 27.3.1" />
              </Form.Item>
              <Form.Item name="compose_version" label="Compose v2 版本（可选）" style={{ flex: 1 }}>
                <Input placeholder="留空使用平台默认固定版本" />
              </Form.Item>
            </Space>

            <Form.Item
              name="registry_mirrors"
              label="Docker 镜像加速器（可选）"
              extra="多个地址用逗号或空格分隔，会合并写入 /etc/docker/daemon.json（先备份）"
            >
              <Input placeholder="https://mirror.gcr.io" />
            </Form.Item>

            {host.sudo_mode === 'password' && (
              <Form.Item name="save_sudo_password" label="sudo 密码（如尚未保存）">
                <Input.Password autoComplete="new-password" placeholder="仅用于 sudo 提权，加密保存" />
              </Form.Item>
            )}

            <Form.Item name="accept_system_changes" valuePropName="checked">
              <Checkbox>
                我了解该操作会在远程主机上安装系统软件包并启用 Docker 服务，属于系统级变更
              </Checkbox>
            </Form.Item>
          </Form>
        )}

        {stage === 'preview' && (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Alert
              type="info"
              showIcon
              message="即将在主机上执行以下脚本"
              description="脚本由平台生成，幂等执行：已满足条件时不会做任何改动，也不会卸载已有的 Compose v1。"
            />
            {preview.isLoading && <Spin />}
            {preview.data && (
              <>
                <Space wrap>
                  <Tag>策略：{preview.data.options.strategy}</Tag>
                  <Tag>Compose 版本：{preview.data.options.compose_version}</Tag>
                  {preview.data.options.mirror && <Tag>镜像源：{preview.data.options.mirror}</Tag>}
                  <Button
                    size="small"
                    icon={<DownloadOutlined />}
                    onClick={() => {
                      const blob = new Blob([preview.data?.script ?? ''], { type: 'text/x-shellscript' })
                      const url = URL.createObjectURL(blob)
                      const a = document.createElement('a')
                      a.href = url
                      a.download = `1mate-install-${host.name}.sh`
                      a.click()
                      URL.revokeObjectURL(url)
                    }}
                  >
                    下载脚本
                  </Button>
                </Space>
                <pre className="code-block">{preview.data.script}</pre>
                <Form form={form} layout="vertical" style={{ marginTop: 8 }}>
                  <Form.Item name="accept_system_changes" valuePropName="checked">
                    <Checkbox>
                      我确认上述脚本内容，并同意在远程主机上执行系统级安装
                    </Checkbox>
                  </Form.Item>
                </Form>
              </>
            )}
            {preview.isError && <Alert type="error" showIcon message={(preview.error as Error).message} />}
          </Space>
        )}

        {stage === 'running' && jobId && (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Alert
              type="info"
              showIcon
              message="安装进行中"
              description="日志实时输出；结束后平台会自动重新探测主机能力。"
            />
            <LogViewer
              jobId={jobId}
              onDone={() => {
                void hostQuery.refetch()
                message.success('安装任务已结束')
              }}
            />
            <Space>
              <Button onClick={() => navigate(`/jobs/${jobId}`)}>在任务中心打开</Button>
              <Button onClick={() => setInstallOpen(false)}>后台运行</Button>
            </Space>
          </Space>
        )}
      </Modal>

      {caps && (
        <Card size="small" title="能力摘要">
          <Space wrap>
            <ComposeKindTag kind={caps.compose_kind} />
            <Tag>Docker {caps.docker_server_version || '未运行'}</Tag>
            <Tag>{caps.arch}</Tag>
            <Tag>{caps.os_pretty || caps.os_id}</Tag>
          </Space>
        </Card>
      )}
    </Space>
  )
}
