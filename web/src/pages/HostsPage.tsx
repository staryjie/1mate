import { useEffect, useState } from 'react'
import { App, Button, Drawer, Form, Input, InputNumber, Radio, Select, Space, Table, Typography } from 'antd'
import { DeleteOutlined, PlusOutlined, ReloadOutlined, ApiOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { api } from '../api/client'
import { useHosts } from '../api/hooks'
import type { ConnectionTest, Host } from '../api/types'
import { HostStatusTag } from '../components/StatusTags'
import { daysAgo } from '../utils/format'

interface HostFormValues {
  name: string
  address: string
  ssh_port: number
  ssh_user: string
  auth_type: 'key' | 'password'
  private_key?: string
  password?: string
  key_passphrase?: string
  sudo_mode: 'none' | 'root' | 'nopasswd' | 'password'
  sudo_password?: string
  trust_fingerprint?: boolean
}

export default function HostsPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { message, modal } = App.useApp()
  const hosts = useHosts()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<Host | null>(null)
  const [form] = Form.useForm<HostFormValues>()
  const [testResult, setTestResult] = useState<ConnectionTest | null>(null)
  const [testing, setTesting] = useState(false)
  const [saving, setSaving] = useState(false)
  const authType = Form.useWatch('auth_type', form)
  const sudoMode = Form.useWatch('sudo_mode', form)

  useEffect(() => {
    if (!open) return
    if (editing) {
      form.setFieldsValue({
        name: editing.name,
        address: editing.address,
        ssh_port: editing.ssh_port,
        ssh_user: editing.ssh_user,
        auth_type: editing.auth_type,
        sudo_mode: editing.sudo_mode,
      })
    } else {
      form.resetFields()
      form.setFieldsValue({ ssh_port: 22, auth_type: 'key', sudo_mode: 'none' })
    }
    setTestResult(null)
  }, [open, editing, form])

  const close = () => {
    setOpen(false)
    setEditing(null)
    setTestResult(null)
  }

  const testConnection = async () => {
    try {
      const values = await form.validateFields(['address', 'ssh_port', 'ssh_user', 'auth_type', 'private_key', 'password', 'key_passphrase'])
      setTesting(true)
      const res = await api.post<ConnectionTest>('/api/v1/hosts/test-connection', {
        address: values.address,
        ssh_port: values.ssh_port,
        ssh_user: values.ssh_user,
        auth_type: values.auth_type,
        private_key: values.private_key ?? '',
        password: values.password ?? '',
        key_passphrase: values.key_passphrase ?? '',
      })
      setTestResult(res)
      if (res.ok) message.success('连接成功')
    } catch (err) {
      if (err instanceof Error && 'errorFields' in err) return
      setTestResult({ ok: false, fingerprint: '', message: (err as Error).message, docker: '' })
      message.error((err as Error).message)
    } finally {
      setTesting(false)
    }
  }

  const save = async () => {
    let values: HostFormValues
    try {
      values = await form.validateFields()
    } catch {
      return
    }
    setSaving(true)
    try {
      const payload: Record<string, unknown> = {
        name: values.name,
        address: values.address,
        ssh_port: values.ssh_port,
        ssh_user: values.ssh_user,
        auth_type: values.auth_type,
        sudo_mode: values.sudo_mode,
      }
      if (values.private_key) payload.private_key = values.private_key
      if (values.password) payload.password = values.password
      if (values.key_passphrase) payload.key_passphrase = values.key_passphrase
      if (values.sudo_password) payload.sudo_password = values.sudo_password
      if (values.trust_fingerprint) payload.trust_fingerprint = true

      if (editing) {
        await api.patch(`/api/v1/hosts/${editing.id}`, payload)
        message.success('主机已更新')
      } else {
        const created = await api.post<Host>('/api/v1/hosts', payload)
        message.success('主机已创建，正在探测环境…')
        await api.post(`/api/v1/hosts/${created.id}/actions/probe`)
      }
      await qc.invalidateQueries({ queryKey: ['hosts'] })
      close()
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const remove = (host: Host) => {
    modal.confirm({
      title: `删除主机 ${host.name}？`,
      content: '仅当该主机上没有实例时才能删除。远程主机上的数据不会被改动。',
      okButtonProps: { danger: true },
      okText: '删除',
      onOk: async () => {
        try {
          await api.del(`/api/v1/hosts/${host.id}`)
          message.success('已删除')
          await qc.invalidateQueries({ queryKey: ['hosts'] })
        } catch (err) {
          message.error((err as Error).message)
        }
      },
    })
  }

  return (
    <>
      <div className="page-header">
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            主机
          </Typography.Title>
          <Typography.Text type="secondary">
            通过 SSH 管理远程主机；平台会在部署前自动探测 Docker 与 Compose。
          </Typography.Text>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => void qc.invalidateQueries({ queryKey: ['hosts'] })}>
            刷新
          </Button>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null)
              setOpen(true)
            }}
          >
            添加主机
          </Button>
        </Space>
      </div>

      <Table<Host>
        rowKey="id"
        loading={hosts.isLoading}
        dataSource={hosts.data?.hosts ?? []}
        onRow={(record) => ({ onClick: () => navigate(`/hosts/${record.id}`), style: { cursor: 'pointer' } })}
        columns={[
          { title: '名称', dataIndex: 'name' },
          {
            title: '地址',
            render: (_, r) => (
              <span className="mono">
                {r.ssh_user}@{r.address}:{r.ssh_port}
              </span>
            ),
          },
          { title: '状态', render: (_, r) => <HostStatusTag status={r.status} /> },
          {
            title: 'Compose',
            render: (_, r) =>
              r.capabilities
                ? `${r.capabilities.compose_kind}${r.capabilities.compose_version ? ` ${r.capabilities.compose_version}` : ''}`
                : '—',
          },
          { title: 'sudo', dataIndex: 'sudo_mode' },
          { title: '实例数', dataIndex: 'instance_count', width: 90 },
          { title: '最近探测', render: (_, r) => daysAgo(r.last_probe_at) },
          {
            title: '操作',
            width: 190,
            render: (_, r) => (
              <Space onClick={(e) => e.stopPropagation()}>
                <Button
                  size="small"
                  icon={<ReloadOutlined />}
                  onClick={async () => {
                    try {
                      await api.post(`/api/v1/hosts/${r.id}/actions/probe`)
                      message.success('已排队探测')
                    } catch (err) {
                      message.error((err as Error).message)
                    }
                  }}
                >
                  探测
                </Button>
                <Button size="small" icon={<ApiOutlined />} onClick={() => navigate(`/hosts/${r.id}`)}>
                  详情
                </Button>
                <Button size="small" danger icon={<DeleteOutlined />} onClick={() => remove(r)} />
              </Space>
            ),
          },
        ]}
      />

      <Drawer
        title={editing ? `编辑主机 · ${editing.name}` : '添加主机'}
        width={560}
        open={open}
        onClose={close}
        destroyOnHidden
        extra={
          <Space>
            <Button onClick={close}>取消</Button>
            <Button type="primary" loading={saving} onClick={() => void save()}>
              保存
            </Button>
          </Space>
        }
      >
        <Form form={form} layout="vertical" requiredMark={false}>
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
            <Input placeholder="例如 home-server" />
          </Form.Item>
          <Form.Item name="address" label="地址" rules={[{ required: true, message: '请输入 IP 或域名' }]}>
            <Input placeholder="192.168.1.10" />
          </Form.Item>
          <Space style={{ display: 'flex' }} align="start">
            <Form.Item name="ssh_port" label="SSH 端口" rules={[{ required: true }]} style={{ width: 140 }}>
              <InputNumber min={1} max={65535} style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="ssh_user" label="SSH 用户" rules={[{ required: true, message: '请输入用户' }]} style={{ flex: 1 }}>
              <Input placeholder="deploy" />
            </Form.Item>
          </Space>
          <Form.Item name="auth_type" label="认证方式">
            <Radio.Group>
              <Radio.Button value="key">私钥</Radio.Button>
              <Radio.Button value="password">密码</Radio.Button>
            </Radio.Group>
          </Form.Item>
          {authType === 'password' ? (
            <Form.Item
              name="password"
              label={editing ? 'SSH 密码（留空则不修改）' : 'SSH 密码'}
              rules={editing ? [] : [{ required: true, message: '请输入密码' }]}
            >
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          ) : (
            <>
              <Form.Item
                name="private_key"
                label={editing ? '私钥（留空则不修改）' : '私钥（PEM / OpenSSH）'}
                rules={editing ? [] : [{ required: true, message: '请粘贴私钥' }]}
              >
                <Input.TextArea rows={5} className="mono" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
              </Form.Item>
              <Form.Item name="key_passphrase" label="私钥口令（可选）">
                <Input.Password autoComplete="new-password" />
              </Form.Item>
            </>
          )}
          <Form.Item name="sudo_mode" label="sudo 方式" extra="安装 Docker 等系统级操作需要提权">
            <Select
              options={[
                { value: 'none', label: '不使用 sudo' },
                { value: 'root', label: '直接使用 root 用户' },
                { value: 'nopasswd', label: 'sudo -n（免密）' },
                { value: 'password', label: 'sudo 需要密码' },
              ]}
            />
          </Form.Item>
          {sudoMode === 'password' && (
            <Form.Item
              name="sudo_password"
              label={editing ? 'sudo 密码（留空则不修改）' : 'sudo 密码'}
              rules={editing ? [] : [{ required: true, message: '请输入 sudo 密码' }]}
              extra="密码仅用于 sudo 提权，通过 stdin 传递，不会出现在命令行或日志中"
            >
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          )}
          {testResult && (
            <Form.Item>
              <Typography.Paragraph type={testResult.ok ? 'success' : 'danger'} style={{ marginBottom: 4 }}>
                {testResult.message}
              </Typography.Paragraph>
              {testResult.fingerprint && (
                <Typography.Text type="secondary" className="mono">
                  指纹：{testResult.fingerprint}
                </Typography.Text>
              )}
              {testResult.docker && (
                <pre className="code-block" style={{ marginTop: 8 }}>
                  {testResult.docker}
                </pre>
              )}
              {editing && testResult.fingerprint && testResult.fingerprint !== editing.host_key_fingerprint && (
                <Form.Item name="trust_fingerprint" valuePropName="checked" style={{ marginTop: 8, marginBottom: 0 }}>
                  <Radio.Group>
                    <Radio value={true}>信任新的主机指纹并更新</Radio>
                  </Radio.Group>
                </Form.Item>
              )}
            </Form.Item>
          )}
          <Space>
            <Button icon={<ApiOutlined />} loading={testing} onClick={() => void testConnection()}>
              测试连接
            </Button>
          </Space>
        </Form>
      </Drawer>
    </>
  )
}
