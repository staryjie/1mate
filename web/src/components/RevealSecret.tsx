import { useState } from 'react'
import { Alert, Button, Input, Modal, Space, Typography } from 'antd'
import { CopyOutlined, EyeInvisibleOutlined, EyeOutlined } from '@ant-design/icons'

interface RevealSecretProps {
  title: string
  onReveal: () => Promise<string>
  description?: string
  buttonText?: string
  warning?: string
}

/**
 * A secret is never rendered until the operator explicitly asks for it, and the
 * reveal is recorded by the API as an audited action.
 */
export default function RevealSecret({ title, onReveal, description, buttonText, warning }: RevealSecretProps) {
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)

  const handleReveal = async () => {
    setLoading(true)
    setError('')
    try {
      const secret = await onReveal()
      setValue(secret)
      setOpen(true)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      setError('浏览器不允许访问剪贴板，请手动复制')
    }
  }

  return (
    <>
      <Button icon={<EyeOutlined />} loading={loading} onClick={() => void handleReveal()} danger>
        {buttonText ?? '查看'}
      </Button>
      {error && !open && (
        <Typography.Text type="danger" style={{ marginLeft: 8 }}>
          {error}
        </Typography.Text>
      )}
      <Modal
        open={open}
        title={title}
        onCancel={() => {
          setOpen(false)
          setValue('')
        }}
        footer={
          <Space>
            <Button icon={<EyeInvisibleOutlined />} onClick={() => setOpen(false)}>
              关闭
            </Button>
            <Button type="primary" icon={<CopyOutlined />} onClick={() => void handleCopy()}>
              {copied ? '已复制' : '复制'}
            </Button>
          </Space>
        }
        destroyOnHidden
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Alert
            type="warning"
            showIcon
            message={warning ?? '此操作已被记录到审计日志。请勿在聊天工具或工单系统中明文传播该密钥。'}
          />
          {description && <Typography.Paragraph type="secondary">{description}</Typography.Paragraph>}
          <Input value={value} readOnly className="mono" />
          {error && <Typography.Text type="danger">{error}</Typography.Text>}
        </Space>
      </Modal>
    </>
  )
}
