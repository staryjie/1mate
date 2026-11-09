export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  const decimals = i === 0 || value >= 10 || Number.isInteger(value) ? 0 : 1
  return `${value.toFixed(decimals)} ${units[i]}`
}

export function formatDateTime(value: string | null | undefined): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString('zh-CN', { hour12: false })
}

export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分钟 ${s % 60} 秒`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时 ${m % 60} 分钟`
  return `${Math.floor(h / 24)} 天 ${h % 24} 小时`
}

export function formatPorts(ports: number[]): string {
  return ports.join(' / ')
}

export function daysAgo(value: string | null | undefined): string {
  if (!value) return '从未'
  return formatDuration(Date.now() - new Date(value).getTime()) + '前'
}
