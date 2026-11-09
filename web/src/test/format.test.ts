import { describe, expect, it } from 'vitest'
import { formatBytes, formatDateTime, formatDuration, daysAgo } from '../utils/format'

describe('format helpers', () => {
  it('formats byte sizes with units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5 MB')
  })

  it('formats durations at each scale', () => {
    expect(formatDuration(5_000)).toBe('5 秒')
    expect(formatDuration(65_000)).toBe('1 分钟 5 秒')
    expect(formatDuration(3_725_000)).toBe('1 小时 2 分钟')
  })

  it('reports never for a missing timestamp', () => {
    expect(daysAgo(null)).toBe('从未')
    expect(daysAgo(undefined)).toBe('从未')
  })

  it('returns a placeholder for invalid dates', () => {
    expect(formatDateTime('not-a-date')).toBe('—')
    expect(formatDateTime(null)).toBe('—')
  })
})
