import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { Button, Result, Spin } from 'antd'
import { api, setCSRFToken } from './api/client'
import { useMe, useSetupStatus } from './api/hooks'
import type { MeResponse } from './api/types'
import AppLayout from './components/AppLayout'
import LoginPage from './pages/LoginPage'
import SetupPage from './pages/SetupPage'
import OverviewPage from './pages/OverviewPage'
import HostsPage from './pages/HostsPage'
import HostDetailPage from './pages/HostDetailPage'
import InstanceWizardPage from './pages/InstanceWizardPage'
import InstanceImportPage from './pages/InstanceImportPage'
import InstanceDetailPage from './pages/InstanceDetailPage'
import InstancesListPage from './pages/InstancesListPage'
import ChangePasswordPage from './pages/ChangePasswordPage'
import JobsPage from './pages/JobsPage'
import SettingsPage from './pages/SettingsPage'

interface AuthState {
  me: MeResponse | null
  loading: boolean
  refresh: () => Promise<void>
  signOut: () => Promise<void>
}

const AuthContext = createContext<AuthState>({
  me: null,
  loading: true,
  refresh: async () => {},
  signOut: async () => {},
})

export function useAuth(): AuthState {
  return useContext(AuthContext)
}

export default function App() {
  const setup = useSetupStatus()
  const me = useMe()
  const navigate = useNavigate()
  const location = useLocation()
  const [meState, setMeState] = useState<MeResponse | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (me.data) {
      setCSRFToken(me.data.csrf_token)
      setMeState(me.data)
      setLoading(false)
      return
    }
    if (me.isError) {
      setMeState(null)
      setLoading(false)
    }
  }, [me.data, me.isError])

  const refresh = useCallback(async () => {
    try {
      const next = await api.get<MeResponse>('/api/v1/auth/me')
      setCSRFToken(next.csrf_token)
      setMeState(next)
    } catch {
      setMeState(null)
    }
  }, [])

  const signOut = useCallback(async () => {
    try {
      await api.post('/api/v1/auth/logout')
    } finally {
      setCSRFToken('')
      setMeState(null)
      navigate('/login', { replace: true })
    }
  }, [navigate])

  const ctx = useMemo<AuthState>(
    () => ({ me: meState, loading, refresh, signOut }),
    [meState, loading, refresh, signOut],
  )

  if (setup.isLoading || (loading && me.isLoading)) {
    return (
      <div className="login-shell">
        <Spin size="large" tip="正在加载…">
          <div style={{ width: 160, height: 60 }} />
        </Spin>
      </div>
    )
  }

  if (setup.data?.setup_required) {
    return (
      <Routes>
        <Route path="*" element={<SetupPage onDone={refresh} />} />
      </Routes>
    )
  }

  const authed = Boolean(meState)
  if (!authed && location.pathname !== '/login') {
    return <Navigate to="/login" state={{ from: location.pathname }} replace />
  }
  if (authed && location.pathname === '/login') {
    return <Navigate to="/" replace />
  }

  return (
    <AuthContext.Provider value={ctx}>
      <Routes>
        <Route path="/login" element={<LoginPage onDone={refresh} />} />
        <Route
          path="*"
          element={
            authed ? (
              <AppLayout>
                <Routes>
                  <Route path="/" element={<OverviewPage />} />
                  <Route path="/hosts" element={<HostsPage />} />
                  <Route path="/hosts/:id" element={<HostDetailPage />} />
                  <Route path="/instances" element={<Navigate to="/instances/new" replace />} />
                  <Route path="/instances/new" element={<InstancesListPage />} />
                  <Route path="/instances/create" element={<InstanceWizardPage />} />
                  <Route path="/instances/import" element={<InstanceImportPage />} />
                  <Route path="/instances/:id" element={<InstanceDetailPage />} />
                  <Route path="/instances/change-password" element={<ChangePasswordPage />} />
                  <Route path="/jobs" element={<JobsPage />} />
                  <Route path="/jobs/:id" element={<JobsPage />} />
                  <Route path="/settings" element={<SettingsPage />} />
                  <Route
                    path="*"
                    element={
                      <Result
                        status="404"
                        title="页面不存在"
                        extra={
                          <Button type="primary" onClick={() => navigate('/')}>
                            返回总览
                          </Button>
                        }
                      />
                    }
                  />
                </Routes>
              </AppLayout>
            ) : (
              <Navigate to="/login" replace />
            )
          }
        />
      </Routes>
    </AuthContext.Provider>
  )
}
