import { useNavigate, useParams } from 'react-router-dom'
import { Alert, App, Button, Card, Empty, Space, Table, Typography } from 'antd'
import { CloseCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { useJob, useJobs } from '../api/hooks'
import { api } from '../api/client'
import { JobStatusTag, jobKindLabel } from '../components/StatusTags'
import LogViewer from '../components/LogViewer'
import { formatDateTime } from '../utils/format'

export default function JobsPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const { message } = App.useApp()
  const jobs = useJobs()
  const job = useJob(id)

  if (id) {
    const current = job.data?.job
    return (
      <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <div className="page-header">
          <div>
            <Typography.Title level={3} style={{ margin: 0 }}>
              任务详情
            </Typography.Title>
            <Typography.Text type="secondary" className="mono">
              {id}
            </Typography.Text>
          </div>
          <Space>
            <Button onClick={() => navigate('/jobs')}>返回列表</Button>
            <Button
              icon={<ReloadOutlined />}
              onClick={() => {
                void job.refetch()
              }}
            >
              刷新
            </Button>
            {(current?.status === 'running' || current?.status === 'queued') && (
              <Button
                danger
                icon={<CloseCircleOutlined />}
                onClick={async () => {
                  try {
                    await api.post(`/api/v1/jobs/${id}/cancel`)
                    message.success('已请求取消')
                    void job.refetch()
                  } catch (err) {
                    message.error((err as Error).message)
                  }
                }}
              >
                取消任务
              </Button>
            )}
          </Space>
        </div>

        {current && (
          <Card size="small">
            <Space wrap size="large">
              <span>类型：{jobKindLabel(current.kind)}</span>
              <span>
                状态：<JobStatusTag status={current.status} />
              </span>
              {current.stage && <span>阶段：{current.stage}</span>}
              <span>创建：{formatDateTime(current.created_at)}</span>
              {current.finished_at && <span>结束：{formatDateTime(current.finished_at)}</span>}
              <span>退出码：{current.exit_code}</span>
            </Space>
            {current.error && (
              <Alert type="error" showIcon style={{ marginTop: 12 }} message={current.error} />
            )}
          </Card>
        )}

        {job.isLoading && <Typography.Text type="secondary">加载中…</Typography.Text>}

        {current && (
          <>
            {(current.status === 'running' || current.status === 'queued') && (
              <LogViewer jobId={current.id} onDone={() => void job.refetch()} />
            )}
            {(current.status !== 'running' && current.status !== 'queued') && (
              <Card size="small" title="日志">
                <pre className="code-block" style={{ maxHeight: 560 }}>
                  {job.data?.log || '（无日志输出）'}
                </pre>
              </Card>
            )}
          </>
        )}
      </Space>
    )
  }

  return (
    <>
      <div className="page-header">
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            任务中心
          </Typography.Title>
          <Typography.Text type="secondary">部署、安装、备份与生命周期操作的历史记录</Typography.Text>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => void jobs.refetch()}>
          刷新
        </Button>
      </div>

      {jobs.data?.jobs.length === 0 ? (
        <Empty description="暂无任务" />
      ) : (
        <Table
          rowKey="id"
          dataSource={jobs.data?.jobs ?? []}
          loading={jobs.isLoading}
          onRow={(record) => ({ onClick: () => navigate(`/jobs/${record.id}`), style: { cursor: 'pointer' } })}
          columns={[
            { title: '类型', dataIndex: 'kind', render: (v: string) => jobKindLabel(v) },
            { title: '状态', render: (_, r) => <JobStatusTag status={r.status} /> },
            { title: '阶段', dataIndex: 'stage', render: (v) => v || '—' },
            { title: '操作者', dataIndex: 'created_by' },
            { title: '创建时间', render: (_, r) => formatDateTime(r.created_at) },
            {
              title: '耗时',
              render: (_, r) => {
                if (!r.started_at) return '—'
                const end = r.finished_at ? new Date(r.finished_at).getTime() : Date.now()
                const secs = Math.max(0, Math.round((end - new Date(r.started_at).getTime()) / 1000))
                return secs < 60 ? `${secs}s` : `${Math.floor(secs / 60)}m ${secs % 60}s`
              },
            },
          ]}
        />
      )}
    </>
  )
}
