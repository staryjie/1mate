import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  Alert,
  App,
  AutoComplete,
  Button,
  Card,
  Col,
  Form,
  Input,
  InputNumber,
  Radio,
  Row,
  Select,
  Space,
  Steps,
  Switch,
  Tag,
  Typography,
} from 'antd'
import { ArrowLeftOutlined, RocketOutlined } from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { useComposePreview, useCreateInstance, useHosts } from '../api/hooks'
import type { Instance } from '../api/types'
import LogViewer from '../components/LogViewer'
import { api } from '../api/client'

const TIMEZONES = [
  'Asia/Shanghai',
  'Asia/Hong_Kong',
  'Asia/Taipei',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Europe/Berlin',
  'Europe/London',
  'America/Los_Angeles',
  'America/New_York',
  'UTC',
]

export default function InstanceWizardPage() {
  const [search] = useSearchParams()
  const navigate = useNavigate()
  const { message } = App.useApp()
  const qc = useQueryClient()
  const hosts = useHosts()
  const create = useCreateInstance()
  const [form] = Form.useForm()
  const [step, setStep] = useState(0)
  const [created, setCreated] = useState<Instance | null>(null)
  const [deployJob, setDeployJob] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [previewEnabled, setPreviewEnabled] = useState(false)

  const hostId = Form.useWatch('host_id', form)
  const slug = Form.useWatch('slug', form)
  const preview = useComposePreview(created?.id, previewEnabled || step === 2)

  useEffect(() => {
    const preset = search.get('host')
    if (preset) form.setFieldsValue({ host_id: preset })
    form.setFieldsValue({
      timezone: 'Asia/Shanghai',
      region: 'global',
      image_tag: 'latest',
      postgres_tag: '18-trixie',
      grafana_admin_user: 'admin',
      bind_address: '0.0.0.0',
      disable_mqtt: false,
    })
  }, [search, form])

  const selectedHost = useMemo(
    () => hosts.data?.hosts.find((h) => h.id === hostId),
    [hosts.data, hostId],
  )

  const hostOptions = useMemo(
    () =>
      (hosts.data?.hosts ?? []).map((h) => ({
        value: h.id,
        label: `${h.name} (${h.address})`,
        disabled: h.status !== 'ok' && h.status !== 'compose_v1',
      })),
    [hosts.data],
  )

  const next = async () => {
    if (step === 0) {
      try {
        await form.validateFields(['host_id'])
        if (!form.getFieldValue('tm_port')) {
          form.setFieldsValue({ tm_port: 4000, grafana_port: 3000, mqtt_port: 1883 })
        }
        setStep(1)
      } catch {
        /* inline errors are enough */
      }
      return
    }
    if (step === 1) {
      let values: Record<string, unknown>
      try {
        values = await form.validateFields()
      } catch {
        return
      }
      const payload = {
        name: values.name,
        slug: values.slug,
        host_id: values.host_id,
        bind_address: values.bind_address || '0.0.0.0',
        tm_port: values.tm_port,
        grafana_port: values.grafana_port,
        mqtt_port: values.mqtt_port,
        timezone: values.timezone,
        region: values.region,
        image_tag: values.image_tag,
        postgres_tag: values.postgres_tag,
        grafana_admin_user: values.grafana_admin_user,
        disable_mqtt: Boolean(values.disable_mqtt),
        extra_env: {},
      }
      if (created) {
        // The record was created on an earlier pass; apply the edited fields
        // before rendering the review step again.
        setSubmitting(true)
        try {
          await api.patch(`/api/v1/instances/${created.id}`, payload)
          await qc.invalidateQueries({ queryKey: ['compose', created.id] })
          await qc.invalidateQueries({ queryKey: ['instance', created.id] })
          setStep(2)
        } catch (err) {
          message.error((err as Error).message)
        } finally {
          setSubmitting(false)
        }
        return
      }
      setSubmitting(true)
      try {
        // The record is created (not deployed) so the review step can render
        // the exact server-side compose and env files for this instance.
        const inst = await create.mutateAsync(payload)
        setCreated(inst)
        setPreviewEnabled(true)
        setStep(2)
      } catch (err) {
        message.error((err as Error).message)
      } finally {
        setSubmitting(false)
      }
      return
    }
    if (step === 2 && created) {
      setSubmitting(true)
      try {
        const res = await api.post<{ job_id: string }>(`/api/v1/instances/${created.id}/actions/deploy`)
        setDeployJob(res.job_id)
        setStep(3)
        message.success('正在部署')
      } catch (err) {
        message.error((err as Error).message)
      } finally {
        setSubmitting(false)
      }
    }
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <div className="page-header">
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            新建 TeslaMate 实例
          </Typography.Title>
          <Typography.Text type="secondary">
            选择主机、配置端口与区域，确认生成的配置后一键部署。
          </Typography.Text>
        </div>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/')}>
          返回
        </Button>
      </div>

      <Steps
        current={step}
        items={[{ title: '选择主机' }, { title: '基本配置' }, { title: '确认部署' }, { title: '部署日志' }]}
        onChange={(s) => {
          if (s < step) setStep(s)
        }}
      />

      {selectedHost && selectedHost.status === 'compose_v1' && (
        <Alert
          type="warning"
          showIcon
          message="该主机只安装了 Compose v1"
          description="平台会使用 docker-compose 命令与旧版语法部署。建议先在主机详情中安装 Compose v2。"
        />
      )}

      <Card>
        <Form form={form} layout="vertical" requiredMark={false} disabled={step >= 2}>
          {step === 0 && (
            <Form.Item name="host_id" label="目标主机" rules={[{ required: true, message: '请选择主机' }]}>
              <Select
                showSearch
                optionFilterProp="label"
                placeholder="选择已探测可用的主机"
                options={hostOptions}
                notFoundContent="暂无可用主机，请先添加并探测"
              />
            </Form.Item>
          )}

          {step === 1 && (
            <Row gutter={16}>
              <Col xs={24} md={12}>
                <Form.Item name="name" label="实例名称" rules={[{ required: true, message: '请输入名称' }]}>
                  <Input placeholder="我的 Model 3" />
                </Form.Item>
              </Col>
              <Col xs={24} md={12}>
                <Form.Item
                  name="slug"
                  label="Slug"
                  tooltip="用于目录名、Compose 项目名，创建后不可修改"
                  rules={[
                    { required: true, message: '请输入 slug' },
                    { pattern: /^[a-z0-9][a-z0-9-]{1,30}$/, message: '2-31 位小写字母、数字或短横线' },
                  ]}
                  extra={
                    slug ? (
                      <>
                        项目名 <span className="inline-code">1mate-{slug}</span>，目录{' '}
                        <span className="inline-code">/opt/1mate/instances/{slug}</span>
                      </>
                    ) : undefined
                  }
                >
                  <Input placeholder="model3-home" />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="tm_port" label="TeslaMate 端口" rules={[{ required: true }]}>
                  <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="grafana_port" label="Grafana 端口" rules={[{ required: true }]}>
                  <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item
                  name="mqtt_port"
                  label="MQTT 端口"
                  rules={[{ required: true }]}
                  tooltip="仅在未关闭 MQTT 时使用"
                >
                  <InputNumber min={1} max={65535} style={{ width: '100%' }} disabled={form.getFieldValue('disable_mqtt')} />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="bind_address" label="监听地址">
                  <Select
                    options={[
                      { value: '127.0.0.1', label: '127.0.0.1（仅本机，推荐配合反代）' },
                      { value: '0.0.0.0', label: '0.0.0.0（所有网卡）' },
                    ]}
                  />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="timezone" label="时区">
                  <AutoComplete options={TIMEZONES.map((v) => ({ value: v }))} filterOption />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="region" label="Tesla API 区域">
                  <Radio.Group>
                    <Radio.Button value="global">国际</Radio.Button>
                    <Radio.Button value="cn">中国</Radio.Button>
                  </Radio.Group>
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="image_tag" label="TeslaMate 镜像标签">
                  <Input placeholder="latest" />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="postgres_tag" label="PostgreSQL 镜像标签" extra="大版本变更需手动迁移">
                  <Input placeholder="18-trixie" />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="grafana_admin_user" label="Grafana 管理员">
                  <Input placeholder="admin" />
                </Form.Item>
              </Col>
              <Col xs={24} md={8}>
                <Form.Item name="disable_mqtt" label="关闭 MQTT" valuePropName="checked">
                  <Switch />
                </Form.Item>
              </Col>
            </Row>
          )}

          {step >= 2 && (
            <Space direction="vertical" size="middle" style={{ width: '100%' }}>
              <Space wrap>
                <Tag>主机：{selectedHost?.name ?? '—'}</Tag>
                {slug && <Tag>项目：1mate-{slug}</Tag>}
                <Tag>Compose：v{selectedHost?.capabilities?.compose_major ?? 2}</Tag>
              </Space>
              {preview.isLoading && <Typography.Text type="secondary">正在生成配置…</Typography.Text>}
              {preview.data && (
                <>
                  <Card size="small" title={`compose.yaml（${preview.data.compose_kind === 'plugin_v2' ? 'docker compose' : 'docker-compose'} 命令）`}>
                    <pre className="code-block">{preview.data.compose}</pre>
                  </Card>
                  <Card size="small" title=".env（敏感值已脱敏）">
                    <pre className="code-block">{preview.data.env}</pre>
                  </Card>
                </>
              )}
              {preview.isError && <Alert type="error" showIcon message={(preview.error as Error).message} />}
            </Space>
          )}
        </Form>
      </Card>

      {step < 3 && (
        <Space>
          {step > 0 && <Button onClick={() => setStep(step - 1)}>上一步</Button>}
          {step === 0 && (
            <Button type="primary" onClick={() => void next()}>
              下一步
            </Button>
          )}
          {step === 1 && (
            <Button type="primary" loading={submitting} onClick={() => void next()}>
              下一步：查看配置
            </Button>
          )}
          {step === 2 && (
            <Button type="primary" icon={<RocketOutlined />} loading={submitting} onClick={() => void next()}>
              创建并部署
            </Button>
          )}
        </Space>
      )}

      {step === 3 && created && deployJob && (
        <Card title="部署日志">
          <Space direction="vertical" style={{ width: '100%' }}>
            {previewEnabled && <Alert type="info" showIcon message="正在实时显示部署日志" />}
            <LogViewer
              jobId={deployJob}
              onDone={() => {
                void preview.refetch()
              }}
            />
            <Space>
              <Button type="primary" onClick={() => navigate(`/instances/${created.id}`)}>
                打开实例详情
              </Button>
              <Button onClick={() => navigate('/')}>返回总览</Button>
            </Space>
          </Space>
        </Card>
      )}
    </Space>
  )
}
