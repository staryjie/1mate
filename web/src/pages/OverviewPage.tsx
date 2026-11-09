import { useNavigate } from 'react-router-dom'
import {
  Alert,
  Button,
  Card,
  Col,
  Empty,
  Row,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
} from 'antd'
import {
  CloudServerOutlined,
  DatabaseOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import { useHosts, useInstances, useJobs } from '../api/hooks'
import type { Host, Instance, Job } from '../api/types'
import { HostStatusTag, InstanceStatusTag, JobStatusTag, jobKindLabel } from '../components/StatusTags'
import { formatDateTime } from '../utils/format'

export default function OverviewPage() {
  const navigate = useNavigate()
  const hosts = useHosts()
  const instances = useInstances()
  const jobs = useJobs()

  const hostList = hosts.data?.hosts ?? []
  const instanceList = instances.data?.instances ?? []
  const jobList = jobs.data?.jobs ?? []

  const running = instanceList.filter((i) => i.status === 'running').length
  const problems = instanceList.filter((i) => i.status === 'failed' || i.status === 'degraded').length
  const hostProblems = hostList.filter((h) => h.status !== 'ok').length

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <div className="page-header">
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            总览
          </Typography.Title>
          <Typography.Text type="secondary">
            管理主机环境并部署多个 TeslaMate 实例
          </Typography.Text>
        </div>
        <Space>
          <Button icon={<CloudServerOutlined />} onClick={() => navigate('/hosts')}>
            管理主机
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/instances/new')}>
            新建实例
          </Button>
        </Space>
      </div>

      {hostList.length === 0 && (
        <Alert
          type="info"
          showIcon
          message="还没有添加主机"
          description={
            <Space direction="vertical">
              <span>先添加一台可 SSH 的 Linux 主机，平台会自动探测按需安装 Docker 与 Compose v2。</span>
              <Button type="primary" size="small" onClick={() => navigate('/hosts')}>
                添加主机
              </Button>
            </Space>
          }
        />
      )}

      <Row gutter={[16, 16]}>
        <Col xs={12} md={6}>
          <Card>
            <Statistic title="主机" value={hostList.length} prefix={<CloudServerOutlined />} />
            {hostProblems > 0 && (
              <Typography.Text type="warning">
                <WarningOutlined /> {hostProblems} 台需要处理
              </Typography.Text>
            )}
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card>
            <Statistic title="实例" value={instanceList.length} prefix={<DatabaseOutlined />} />
            <Typography.Text type="secondary">{running} 个运行中</Typography.Text>
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card>
            <Statistic
              title="异常实例"
              value={problems}
              valueStyle={problems > 0 ? { color: '#cf1322' } : undefined}
              prefix={<WarningOutlined />}
            />
          </Card>
        </Col>
        <Col xs={12} md={6}>
          <Card>
            <Statistic
              title="进行中任务"
              value={jobList.filter((j) => j.status === 'running' || j.status === 'queued').length}
              prefix={<PlayCircleOutlined />}
            />
          </Card>
        </Col>
      </Row>

      <Card title="主机" extra={<Button size="small" onClick={() => navigate('/hosts')}>全部</Button>}>
        {hostList.length === 0 ? (
          <Empty description="暂无主机" />
        ) : (
          <Table<Host>
            size="small"
            rowKey="id"
            pagination={false}
            dataSource={hostList}
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
                  r.capabilities ? (
                    <Tag color={r.capabilities.compose_major === 2 ? 'green' : r.capabilities.compose_major === 1 ? 'gold' : 'default'}>
                      {r.capabilities.compose_kind === 'none'
                        ? '未安装'
                        : `${r.capabilities.compose_kind.replace('_', ' ')} ${r.capabilities.compose_version}`}
                    </Tag>
                  ) : (
                    <Typography.Text type="secondary">未探测</Typography.Text>
                  ),
              },
              { title: '实例数', dataIndex: 'instance_count', width: 90 },
            ]}
          />
        )}
      </Card>

      <Card title="实例" extra={<Button size="small" onClick={() => navigate('/instances/new')}>新建</Button>}>
        {instanceList.length === 0 ? (
          <Empty description="暂无实例" />
        ) : (
          <Table<Instance>
            size="small"
            rowKey="id"
            pagination={false}
            dataSource={instanceList}
            onRow={(record) => ({
              onClick: () => navigate(`/instances/${record.id}`),
              style: { cursor: 'pointer' },
            })}
            columns={[
              { title: '名称', dataIndex: 'name' },
              { title: 'Slug', dataIndex: 'slug', render: (v) => <span className="mono">{v}</span> },
              { title: '状态', render: (_, r) => <InstanceStatusTag status={r.status} /> },
              {
                title: '端口',
                render: (_, r) => (
                  <span className="mono">
                    {r.tm_port} / {r.grafana_port}
                    {r.disable_mqtt ? '' : ` / ${r.mqtt_port}`}
                  </span>
                ),
              },
              {
                title: 'Compose',
                render: (_, r) => <Tag color={r.compose_major === 2 ? 'green' : 'gold'}>v{r.compose_major}</Tag>,
              },
              { title: '更新时间', render: (_, r) => formatDateTime(r.updated_at) },
            ]}
          />
        )}
      </Card>

      <Card title="最近任务" extra={<Button size="small" onClick={() => navigate('/jobs')}>全部</Button>}>
        {jobList.length === 0 ? (
          <Empty description="暂无任务" />
        ) : (
          <Table<Job>
            size="small"
            rowKey="id"
            pagination={false}
            dataSource={jobList.slice(0, 8)}
            onRow={(record) => ({ onClick: () => navigate(`/jobs/${record.id}`), style: { cursor: 'pointer' } })}
            columns={[
              { title: '类型', dataIndex: 'kind', render: (v: string) => jobKindLabel(v) },
              { title: '状态', render: (_, r) => <JobStatusTag status={r.status} /> },
              { title: '阶段', dataIndex: 'stage', render: (v) => v || '—' },
              { title: '创建时间', render: (_, r) => formatDateTime(r.created_at) },
            ]}
          />
        )}
      </Card>
    </Space>
  )
}
