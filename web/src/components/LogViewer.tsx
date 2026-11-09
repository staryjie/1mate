import { useEffect, useMemo, useRef } from 'react'
import { openJobStream } from '../api/client'

interface Props {
  jobId: string
  onDone?: (job: unknown) => void
  height?: number
  maxChars?: number
}

function classify(line: string): string {
  const t = line.trim()
  if (t.startsWith('== ') || t.startsWith('1MATE_STAGE=')) return 'stage'
  if (t.startsWith('1MATE_ERROR=') || t.toLowerCase().includes('error') || t.toLowerCase().includes('failed')) {
    return 'error'
  }
  if (t.startsWith('1MATE_OK=1')) return 'ok'
  return ''
}

/**
 * Live job log. It replays from the server's buffer, follows new output and
 * stops on the terminal event. Lines are appended incrementally so a long
 * install does not re-render the whole transcript on every chunk.
 */
export default function LogViewer({ jobId, onDone, height, maxChars = 400_000 }: Props) {
  const boxRef = useRef<HTMLDivElement | null>(null)
  const linesRef = useRef<string[]>([])
  const charsRef = useRef(0)
  const doneRef = useRef(onDone)
  doneRef.current = onDone

  useEffect(() => {
    linesRef.current = []
    charsRef.current = 0
    if (boxRef.current) boxRef.current.innerHTML = ''
    const el = boxRef.current
    if (!el) return

    const append = (text: string) => {
      const chunk = text.replace(/\r/g, '')
      for (const raw of chunk.split('\n')) {
        if (raw === '') continue
        charsRef.current += raw.length
        const cls = classify(raw)
        const div = document.createElement('div')
        if (cls) div.className = cls
        div.textContent = raw
        el.appendChild(div)
        linesRef.current.push(raw)
      }
      while (charsRef.current > maxChars && el.firstChild) {
        const first = el.firstChild as HTMLElement
        const removed = (first.textContent ?? '').length
        el.removeChild(first)
        charsRef.current -= removed
      }
      el.scrollTop = el.scrollHeight
    }

    const close = openJobStream(jobId, {
      onLine: append,
      onDone: (job) => {
        append('— 任务已结束 —')
        doneRef.current?.(job)
      },
      onError: () => append('— 日志流已断开，请稍后重新打开 —'),
    })

    return () => close()
  }, [jobId, maxChars])

  const style = useMemo<{ height?: number }>(() => (height ? { height } : {}), [height])

  return <div className="log-viewer" ref={boxRef} style={style} role="log" aria-live="polite" />
}
