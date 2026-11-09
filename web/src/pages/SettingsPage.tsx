import { useEffect } from 'react'
import { Alert, App, Button, Card, Form, Input, Select, Space, Table, Tabs, Tag, Typography } from 'antd'
import { useAuditLogs, useSettings, useUpdateSettings } from '../api/hooks'
import { useAuth } from '../App'
import { formatDateTime } from '../utils/format'

export default function SettingsPage() {
  const { me } = useAuth()
  const { message } = App.useApp()
  const settings = useSettings()
  const update = useUpdateSettings()
  const audit = useAuditLogs()
  const [form] = Form.useForm()

  useEffect(() => {
    if (settings.data) {
      form.setFieldsValue({
        require_compose_v2: settings.data.settings.require_compose_v2 ?? 'false',
        compose_binary_version: settings.data.settings.compose_binary_version ?? '2.29.7',
        default_pkg_mirror: settings.data.settings.default_pkg_mirror ?? 'none',
        compose_download_base_url: settings.data.settings.compose_download_base_url ?? '',
      })
    }
  }, [settings.data, form])

  const save = async () => {
    try {
      const values = await form.validateFields()
      await update.mutateAsync(values)
      message.success('设置已保存')
    } catch (err) {
      message.error((err as Error).message)
    }
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <div className="page-header">
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            设置与审计
          </Typography.Title>
          <Typography.Text type="secondary">平台级策略、安装默认值与操作审计</Typography.Text>
        </div>
      </div>

      <Tabs
        items={[
          {
            key: 'platform',
            label: '平台设置',
            children: (
              <Card>
                <Form form={form} layout="vertical" style={{ maxWidth: 560 }}>
                  <Form.Item
                    name="require_compose_v2"
                    label="强制使用 Docker Compose v2"
                    extra="开启后，只有 v1 的主机将无法创建新实例或部署"
                  >
                    <Select
                      options={[
                        { value: 'false', label: '否（允许 v1，界面会显示告警）' },
                        { value: 'true', label: '是（v1 主机将被拒绝）' },
                      ]}
                    />
                  </Form.Item>
                  <Form.Item
                    name="compose_binary_version"
                    label="Docker Compose v2 安装版本"
                    extra="平台在主机缺少 Compose 时会安装该固定版本，不依赖 GitHub API 实时查询"
                  >
                    <Input placeholder="2.29.7" />
                  </Form.Item>
                  <Form.Item
                    name="compose_download_base_url"
                    label="Compose 下载地址前缀（可选）"
                    extra="留空使用 GitHub Releases；内网环境可改为镜像地址"
                  >
                    <Input placeholder="https://github.com/docker/compose/releases/download" />
                  </Form.Item>
                  <Form.Item
                    name="default_pkg_mirror"
                    label="默认软件源镜像"
                    extra="安装 Docker 时默认使用的镜像源"
                  >
                    <Select
                      options={[
                        { value: 'none', label: '官方源' },
                        { value: 'Aliyun', label: '阿里云' },
                        { value: 'AzureChinaCloud', label: 'Azure 中国' },
                      ]}
                    />
                  </Form.Item>
                  <Button type="primary" onClick={() => void save()} loading={update.isPending}>
                    保存设置
                  </Button>
                </Form>
              </Card>
            ),
          },
          {
            key: 'audit',
            label: '审计日志',
            children: (
              <Card>
                <Table
                  size="small"
                  rowKey="id"
                  loading={audit.isLoading}
                  dataSource={audit.data?.logs ?? []}
                  pagination={{ pageSize: 20 }}
                  columns={[
                    { title: '时间', render: (_, r) => formatDateTime(r.created_at), width: 180 },
                    { title: '操作者', dataIndex: 'actor', width: 120 },
                    { title: '动作', dataIndex: 'action', render: (v: string) => <Tag>{v}</Tag> },
                    { title: '对象', render: (_, r) => `${r.target_type} ${r.target_id}`.trim() },
                    {
                      title: '详情',
                      dataIndex: 'detail',
                      render: (v: string) => (
                        <Typography.Text className="mono" style={{ fontSize: 12 }}>
                          {v === '{}' ? '—' : v}
                        </Typography.Text>
                      ),
                    },
                    { title: '来源 IP', dataIndex: 'ip' },
                  ]}
                />
              </Card>
            ),
          },
          {
            key: 'about',
            label: '关于',
            children: (
              <Card>
                <Space direction="vertical">
                  <Typography.Text>
                    当前用户：<strong>{me?.user.username}</strong>
                  </Typography.Text>
                  <Typography.Text>版本：{me?.version}</Typography.Text>
                  <Alert
                    type="warning"
                    showIcon
                    message="请备份主密钥"
                    description="数据目录下的 master.key（或环境变量 ONEMATE_MASTER_KEY）用于解密已保存的 SSH 凭据与实例密钥；丢失后无法恢复。"
                  />
                  <Typography.Text type="secondary">
                    平台默认仅监听 127.0.0.1。对外提供服务请使用 TLS 反向代理，并开启 ONEMATE_SECURE_COOKIE=true。
                  </Typography.Text>
                </Space>
              </Card>
            ),
          },
        ]}
      />
    </Space>
  )
}
