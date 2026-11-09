import { useState } from 'react'
import { Alert, Button, Card, Form, Input, Typography } from 'antd'
import { api, setCSRFToken } from '../api/client'
import type { MeResponse } from '../api/types'

export default function SetupPage({ onDone }: { onDone: () => Promise<void> }) {
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const submit = async (values: { username: string; password: string; confirm: string }) => {
    setLoading(true)
    setError('')
    try {
      const res = await api.post<MeResponse>('/api/v1/auth/setup', {
        username: values.username,
        password: values.password,
      })
      setCSRFToken(res.csrf_token)
      await onDone()
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="login-shell">
      <Card className="login-card" title="初始化 1mate">
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message="首次启动"
          description="请创建管理员账户。平台会在数据目录生成加密主密钥，请务必自行备份，否则已保存的密钥将无法解密。"
        />
        {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 16 }} />}
        <Form layout="vertical" onFinish={submit} requiredMark={false}>
          <Form.Item
            name="username"
            label="用户名"
            rules={[
              { required: true, message: '请输入用户名' },
              { pattern: /^[a-zA-Z0-9][a-zA-Z0-9_.-]{2,31}$/, message: '3-32 位字母数字，可含 . _ -' },
            ]}
          >
            <Input autoComplete="username" autoFocus />
          </Form.Item>
          <Form.Item
            name="password"
            label="密码"
            rules={[{ required: true, min: 10, message: '密码至少 10 位' }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="confirm"
            label="确认密码"
            dependencies={['password']}
            rules={[
              { required: true, message: '请再次输入密码' },
              ({ getFieldValue }) => ({
                validator: (_, value) =>
                  !value || getFieldValue('password') === value
                    ? Promise.resolve()
                    : Promise.reject(new Error('两次输入的密码不一致')),
              }),
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>
            创建管理员并登录
          </Button>
        </Form>
        <Typography.Paragraph type="secondary" style={{ marginTop: 16, marginBottom: 0 }}>
          生产环境请通过 TLS 反向代理访问，并限制来源 IP。
        </Typography.Paragraph>
      </Card>
    </div>
  )
}
