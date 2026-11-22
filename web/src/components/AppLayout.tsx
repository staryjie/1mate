import { useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import {
  AppstoreOutlined,
  CloudServerOutlined,
  HistoryOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  SettingOutlined,
  ThunderboltOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { Button, Dropdown, Layout, Menu, Space, Tag, Typography } from 'antd'
import { useAuth } from '../App'

const { Header, Sider, Content } = Layout

const items = [
  { key: '/', icon: <AppstoreOutlined />, label: <Link to="/">总览</Link> },
  { key: '/hosts', icon: <CloudServerOutlined />, label: <Link to="/hosts">主机</Link> },
  { key: '/instances/new', icon: <ThunderboltOutlined />, label: <Link to="/instances/new">实例</Link> },
  { key: '/jobs', icon: <HistoryOutlined />, label: <Link to="/jobs">任务</Link> },
  { key: '/settings', icon: <SettingOutlined />, label: <Link to="/settings">设置</Link> },
]

export default function AppLayout({ children }: { children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(false)
  const location = useLocation()
  const navigate = useNavigate()
  const { me, signOut } = useAuth()

  const selected = (() => {
    const p = location.pathname
    if (p.startsWith('/instances/')) return '/instances/new'
    if (p.startsWith('/hosts')) return '/hosts'
    if (p.startsWith('/jobs')) return '/jobs'
    if (p.startsWith('/settings')) return '/settings'
    return '/'
  })()

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider collapsible collapsed={collapsed} onCollapse={setCollapsed} breakpoint="lg" width={216}>
        <div style={{ height: 56, display: 'flex', alignItems: 'center', gap: 8, padding: '0 16px' }}>
          <ThunderboltOutlined style={{ color: '#1677ff', fontSize: 20 }} />
          {!collapsed && (
            <Typography.Text strong style={{ color: '#fff', fontSize: 16 }}>
              1mate
            </Typography.Text>
          )}
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[selected]}
          items={items}
          onClick={({ key }) => navigate(key)}
        />
      </Sider>
      <Layout>
        <Header
          style={{
            background: '#fff',
            padding: '0 20px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            borderBottom: '1px solid #f0f0f0',
            height: 56,
          }}
        >
          <Space>
            <Button
              type="text"
              icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
              onClick={() => setCollapsed((v) => !v)}
              aria-label={collapsed ? '展开菜单' : '收起菜单'}
            />
            <Typography.Text strong>TeslaMate 多实例部署平台</Typography.Text>
          </Space>
          <Space>
            <Tag color="blue">v{me?.version ?? 'dev'}</Tag>
            <Dropdown
              menu={{
                items: [
                  {
                    key: 'change_password',
                    icon: <UserOutlined />,
                    label: '修改密码',
                    onClick: () => navigate('/instances/change-password'),
                  },
                  {
                    key: 'logout',
                    icon: <LogoutOutlined />,
                    label: '退出登录',
                    onClick: () => void signOut(),
                  },
                ],
              }}
            >
              <Button type="text">{me?.user.username ?? '账户'}</Button>
            </Dropdown>
          </Space>
        </Header>
        <Content style={{ padding: 20, maxWidth: 1440, width: '100%', margin: '0 auto' }}>{children}</Content>
      </Layout>
    </Layout>
  )
}
