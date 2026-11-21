import { useState } from 'react'
import { App, Button, Card, Form, Input, Space, Typography } from 'antd'
import { useChangePassword } from '../api/hooks'
import { useAuth } from '../App'

interface ChangePasswordForm {
  old_password: string
  new_password: string
  confirm_password: string
}

export default function ChangePasswordPage() {
  const { me } = useAuth()
  const { message } = App.useApp()
  const change = useChangePassword()
  const [form] = Form.useForm<ChangePasswordForm>()
  const [autoLoggedOut, setAutoLoggedOut] = useState(false)

  const save = async () => {
    try {
      const values = await form.validateFields()
      if (values.new_password !== values.confirm_password) {
        form.setFields([{ name: 'confirm_password', errors: ['两次输入的密码不一致'] }])
        return
      }
      await change.mutateAsync({
        old_password: values.old_password,
        new_password: values.new_password,
      })
      message.success('密码已修改，请重新登录')
      setAutoLoggedOut(true)
    } catch (err) {
      message.error((err as Error).message)
    }
  }

  if (autoLoggedOut) {
    return (
      <Space direction="vertical" align="center" style={{ width: '100%', minHeight: '60vh' }}>
        <Card>
          <Typography.Title level={3}>密码已修改</Typography.Title>
          <Typography.Text type="secondary">
            出于安全考虑，所有会话已失效。请使用新密码重新登录。
          </Typography.Text>
          <Button type="primary" onClick={() => void window.location.assign('/login')}>
            前往登录
          </Button>
        </Card>
      </Space>
    )
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <div className="page-header">
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            修改密码
          </Typography.Title>
          <Typography.Text type="secondary">
            当前用户：<strong>{me?.user?.username ?? '—'}</strong>
          </Typography.Text>
        </div>
      </div>

      <Card>
        <Form form={form} layout="vertical" style={{ maxWidth: 480 }} onFinish={save}>
          <Form.Item
            name="old_password"
            label="当前密码"
            rules={[{ required: true, message: '请输入当前密码' }]}
          >
            <Input.Password />
          </Form.Item>
          <Form.Item
            name="new_password"
            label="新密码"
            extra="密码长度至少 10 个字符"
            rules={[
              { required: true, message: '请输入新密码' },
              { min: 10, message: '密码长度至少 10 个字符' },
            ]}
          >
            <Input.Password />
          </Form.Item>
          <Form.Item
            name="confirm_password"
            label="确认新密码"
            rules={[{ required: true, message: '请确认新密码' }]}
          >
            <Input.Password />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={change.isPending}>
              修改密码
            </Button>
          </Form.Item>
        </Form>
      </Card>
    </Space>
  )
}
