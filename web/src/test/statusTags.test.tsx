import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { describe as describeStatus } from 'vitest'
import { ComposeKindTag, HostStatusTag, InstanceStatusTag, JobStatusTag } from '../components/StatusTags'

describe('status tags', () => {
  it('renders every host status with a label', () => {
    render(
      <div>
        <HostStatusTag status="ok" />
        <HostStatusTag status="no_docker" />
        <HostStatusTag status="compose_v1" />
        <HostStatusTag status="host_key_changed" />
      </div>,
    )
    expect(screen.getByText('可用')).toBeInTheDocument()
    expect(screen.getByText('未安装 Docker')).toBeInTheDocument()
    expect(screen.getByText('仅 Compose v1')).toBeInTheDocument()
    expect(screen.getByText('主机密钥变更')).toBeInTheDocument()
  })

  it('renders instance and job states', () => {
    render(
      <div>
        <InstanceStatusTag status="running" />
        <InstanceStatusTag status="degraded" />
        <JobStatusTag status="failed" />
      </div>,
    )
    expect(screen.getByText('运行中')).toBeInTheDocument()
    expect(screen.getByText('部分异常')).toBeInTheDocument()
    expect(screen.getByText('失败')).toBeInTheDocument()
  })

  it('marks compose v1 as end of life', () => {
    render(<ComposeKindTag kind="standalone_v1" />)
    expect(screen.getByText(/已 EOL/)).toBeInTheDocument()
  })
})

describeStatus('compose version priority', () => {
  it('treats plugin and standalone v2 the same way', () => {
    const { container } = render(
      <div>
        <ComposeKindTag kind="plugin_v2" />
        <ComposeKindTag kind="standalone_v2" />
      </div>,
    )
    expect(container.textContent).toContain('v2 插件')
    expect(container.textContent).toContain('v2 独立版')
  })
})
