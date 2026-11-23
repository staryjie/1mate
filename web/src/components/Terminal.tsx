import { useEffect, useRef, useState, useCallback } from 'react'
import { Terminal as XTerm } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import 'xterm/css/xterm.css'
import { Alert, Button, Space } from 'antd'
import { PoweroffOutlined, LogoutOutlined } from '@ant-design/icons'

interface TerminalProps {
  hostId: string
}

const WS_PROTOCOL = window.location.protocol === 'https:' ? 'wss:' : 'ws:'

export default function Terminal({ hostId }: TerminalProps) {
  const [connected, setConnected] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const termRef = useRef<XTerm | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const wasConnectedRef = useRef(false)

  const connect = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) return
    setConnecting(true)
    setError(null)

    const wsUrl = `${WS_PROTOCOL}//${window.location.host}/api/v1/hosts/${hostId}/terminal`
    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => {
      wasConnectedRef.current = true
      setConnected(true)
      setConnecting(false)
      setError(null)
      // Initialize terminal after connection
      if (!termRef.current) {
        initTerminal()
      }
    }

    ws.onmessage = (event) => {
      if (typeof event.data !== 'string') return
      try {
        const msg = JSON.parse(event.data)
        if (msg.type === 'output' && termRef.current) {
          const data = atob(msg.data)
          termRef.current.write(data)
        } else if (msg.type === 'exit') {
          setConnected(false)
          setError(`终端会话已结束 (退出码: ${msg.code ?? '?'})`)
        }
      } catch {
        /* ignore malformed messages */
      }
    }

    ws.onerror = (err) => {
      setConnecting(false)
      setError('WebSocket 连接失败')
      console.error('[1mate Terminal] WebSocket error:', err)
      console.error('[1mate Terminal] WS URL:', wsUrl)
      console.error('[1mate Terminal] readyState:', ws.readyState)
    }

    ws.onclose = (event) => {
      setConnected(false)
      setConnecting(false)
      if (event.code !== 1000 && event.code !== 1006) {
        console.error('[1mate Terminal] WebSocket closed abnormally:', {
          code: event.code,
          reason: event.reason,
          wasClean: event.wasClean,
        })
      }
    }
  }, [hostId])

  const initTerminal = useCallback(() => {
    const term = new XTerm({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: '"Cascadia Code", "Consolas", "Monaco", monospace',
      theme: { background: '#0f0f0f' },
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(containerRef.current!)
    termRef.current = term
    fitAddonRef.current = fitAddon
    fitAddon.fit()

    term.onData((data) => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        const payload = JSON.stringify({
          type: 'input',
          data: btoa(data),
        })
        wsRef.current.send(payload)
      }
    })

    // Initial fit
    fitAddon.fit()
  }, [])

  useEffect(() => {
    connect()
    return () => {
      if (wsRef.current) {
        wsRef.current.close()
      }
      if (termRef.current) {
        termRef.current.dispose()
        termRef.current = null
      }
    }
  }, [connect])

  // Handle window resize
  useEffect(() => {
    const handleResize = () => {
      if (fitAddonRef.current && termRef.current) {
        fitAddonRef.current.fit()
        const { rows, cols } = termRef.current
        if (wsRef.current?.readyState === WebSocket.OPEN) {
          wsRef.current.send(
            JSON.stringify({ type: 'resize', rows, cols }),
          )
        }
      }
    }
    window.addEventListener('resize', handleResize)
    return () => window.removeEventListener('resize', handleResize)
  }, [])

  const disconnect = useCallback(() => {
    if (wsRef.current) {
      wsRef.current.close()
      wsRef.current = null
    }
    setConnected(false)
    setConnecting(false)
    setError(null)
    if (termRef.current) {
      termRef.current.dispose()
      termRef.current = null
    }
    fitAddonRef.current = null
  }, [])

  const reconnect = useCallback(() => {
    disconnect()
    setTimeout(connect, 500)
  }, [disconnect, connect])

  if (!connected && !connecting && !error && !wasConnectedRef.current) {
    return null
  }

  return (
    <Space direction="vertical" style={{ width: '100%', height: '100%' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: 8 }}>
        <span
          style={{
            display: 'inline-block',
            width: 8,
            height: 8,
            borderRadius: '50%',
            backgroundColor: connected ? '#52c41a' : '#faad14',
          }}
        ></span>
        <span>{connected ? '终端已连接' : connecting ? '正在连接…' : '未连接'}</span>
        {connected ? (
          <Button
            size="small"
            onClick={disconnect}
            danger
            icon={<LogoutOutlined />}
          >
            断开连接
          </Button>
        ) : null}
        <Button
          size="small"
          onClick={reconnect}
          icon={<PoweroffOutlined />}
        >
          重新连接
        </Button>
      </div>
      {error && (
        <Alert type="error" showIcon message="终端错误" description={error} />
      )}
      <div
        ref={containerRef}
        style={{
          flex: 1,
          minHeight: 400,
          border: '1px solid #d9d9d9',
          borderRadius: 4,
          background: '#0f0f0f',
        }}
      />
    </Space>
  )
}