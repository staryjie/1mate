import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Alert, Button, Card, Form, Input, Typography, App } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { api, setCSRFToken } from '../api/client'
import type { MeResponse } from '../api/types'

export default function LoginPage({ onDone }: { onDone: () => Promise<void> }) {
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const navigate = useNavigate()
  const { message } = App.useApp()

  const submit = async (values: { username: string; password: string }) => {
    setLoading(true)
    setError('')
    try {
      const res = await api.post<MeResponse>('/api/v1/auth/login', values)
      setCSRFToken(res.csrf_token)
      await onDone()
      message.success(`欢迎回来，${res.user.username}`)
      navigate('/', { replace: true })
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="login-shell">
      <Card className="login-card" title="登录 1mate">
        <Typography.Paragraph type="secondary">
          使用管理员账户登录，管理多台主机上的 TeslaMate 实例。
        </Typography.Paragraph>
        {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 16 }} />}
        <Form layout="vertical" onFinish={submit} requiredMark={false}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined />} autoComplete="username" autoFocus />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading}>
            登录
          </Button>
        </Form>
      </Card>
    </div>
  )
}
