import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  App,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Radio,
  Select,
  Typography,
} from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { useHosts, useImportInstance } from '../api/hooks'

const { Text } = Typography

export default function InstanceImportPage() {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { message } = App.useApp()
  const qc = useQueryClient()
  const hosts = useHosts()
  const importInstance = useImportInstance()
  const [form] = Form.useForm()
  const [submitting, setSubmitting] = useState(false)

  const presetHostId = searchParams.get('host') || undefined

  const eligibleHosts = useMemo(
    () =>
      hosts.data?.hosts.filter(
        (h) => h.status === 'ok' && h.capabilities?.compose_kind !== 'none',
      ) ?? [],
    [hosts.data],
  )

  useEffect(() => {
    form.setFieldsValue({
      compose_major: 2,
      timezone: 'Asia/Shanghai',
      bind_address: '0.0.0.0',
      region: 'cn',
      ...(presetHostId ? { host_id: presetHostId } : {}),
    })
  }, [form, presetHostId])

  const toNum = (v: any): number | undefined =>
    v == null || v === '' ? undefined : Number(v)

  const onFinish = async (values: any) => {
    setSubmitting(true)
    try {
      const inst = await importInstance.mutateAsync({
        name: values.name,
        slug: values.slug,
        host_id: values.host_id,
        base_dir: values.base_dir,
        compose_major: toNum(values.compose_major),
        tm_port: toNum(values.tm_port),
        grafana_port: toNum(values.grafana_port),
        mqtt_port: toNum(values.mqtt_port),
        region: values.region || undefined,
      })
      message.success(`实例 "${inst.name}" 导入成功`)
      void qc.invalidateQueries({ queryKey: ['instances'] })
      navigate(`/instances/${inst.id}`)
    } catch (err: any) {
      message.error(err?.response?.data?.message || '导入失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card style={{ maxWidth: 720, margin: '24px auto' }}
      title="导入现有实例"
      extra={
        <Button
          icon={<ArrowLeftOutlined />}
          onClick={() => navigate('/')}
          style={{ marginRight: 8 }}
        >
          返回
        </Button>
      }>
      <Form form={form} layout="vertical" onFinish={onFinish}>
        <Form.Item
          label="宿主机"
          name="host_id"
          rules={[{ required: true, message: '请选择宿主机' }]}
        >
          <Select
            placeholder="选择已探测通过的宿主机"
            disabled={eligibleHosts.length === 0}
          >
            {eligibleHosts.map((h) => (
              <Select.Option key={h.id} value={h.id}>
                {h.name} ({h.address})
                {h.capabilities ? (
                  <Text style={{ color: 'rgba(0,0,0,0.45)', fontSize: 12 }}>
                    {' '}· {h.capabilities.os_pretty} · {h.capabilities.compose_kind} {h.capabilities.compose_version}
                  </Text>
                ) : null}
              </Select.Option>
            ))}
          </Select>
        </Form.Item>

        <Form.Item
          label="实例名称"
          name="name"
          rules={[{ required: true, message: '请输入实例名称' }]}
        >
          <Input placeholder="例如：我的 Tesla" />
        </Form.Item>

        <Form.Item
          label="实例标识 (slug)"
          name="slug"
          rules={[
            { required: true, message: '请输入 slug' },
            { pattern: /^[a-z0-9][a-z0-9-]{1,30}$/, message: '只能包含小写字母、数字和连字符，且以字母或数字开头' },
          ]}
        >
          <Input placeholder="例如：my-tesla" />
        </Form.Item>

        <Form.Item
          label="远程 Compose 目录"
          name="base_dir"
          rules={[{ required: true, message: '请输入远程 compose 目录' }]}
        >
          <Input placeholder="/opt/1mate/instances/my-tesla" />
        </Form.Item>

        <Form.Item label="Compose 主版本" name="compose_major" valuePropName="value">
          <Radio.Group>
            <Radio value={0}>自动（从宿主机推断）</Radio>
            <Radio value={1}>Compose v1</Radio>
            <Radio value={2}>Compose v2</Radio>
          </Radio.Group>
        </Form.Item>

        <Form.Item label="TeslaMate 端口" name="tm_port">
          <InputNumber placeholder="4000" min={1} max={65535} />
        </Form.Item>
        <Form.Item label="Grafana 端口" name="grafana_port">
          <InputNumber placeholder="3000" min={1} max={65535} />
        </Form.Item>
        <Form.Item label="MQTT 端口" name="mqtt_port">
          <InputNumber placeholder="1883" min={1} max={65535} />
        </Form.Item>

        <Form.Item label="Tesla 服务器区域" name="region" valuePropName="value">
          <Radio.Group>
            <Radio value="cn">中国</Radio>
            <Radio value="global">国际</Radio>
          </Radio.Group>
        </Form.Item>

        {eligibleHosts.length === 0 && (
          <Text type="warning">
            没有状态为 ok 且已安装 Docker Compose 的宿主机。请先添加宿主机并完成探测。
          </Text>
        )}

        <Form.Item>
          <Button
            htmlType="submit"
            type="primary"
            disabled={eligibleHosts.length === 0 || submitting}
            loading={submitting}
          >
            导入实例
          </Button>
        </Form.Item>
      </Form>
    </Card>
  )
}
