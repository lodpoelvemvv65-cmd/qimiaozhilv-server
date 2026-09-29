import { useQuery } from '@tanstack/react-query'
import { Navigate, Route, Routes } from 'react-router-dom'
import { api } from '../api/client'
import { canAny, Shell } from '../layouts/Shell'
import { Login } from '../pages/Login/Login'
import { Dashboard } from '../pages/Dashboard/Dashboard'
import { Players } from '../pages/Players/Players'
import { PlayerDetail } from '../pages/PlayerDetail/PlayerDetail'
import { Configs } from '../pages/Configs/Configs'
import { Audit } from '../pages/Audit/Audit'
import { Operations } from '../pages/Operations/Operations'
import { SubGMs } from '../pages/SubGMs/SubGMs'
import { Broadcast } from '../pages/Broadcast/Broadcast'

export default function App() {
  const me = useQuery({ queryKey: ['me'], queryFn: api.me, retry: false })
  if (me.isLoading) return <div className="boot-screen"><div className="spinner" /><span>正在连接管理服务…</span></div>
  const authenticated = !!me.data
  const subGM = me.data?.roles.includes('subgm') === true
  return <Routes>
    <Route path="/login" element={authenticated ? <Navigate to="/" replace /> : <Login />} />
    <Route element={authenticated ? <Shell session={me.data!} /> : <Navigate to="/login" replace />}>
      <Route index element={subGM ? <Navigate to="/players" replace /> : <Dashboard />} />
      <Route path="players" element={<Players />} />
      <Route path="players/:playerId" element={<PlayerDetail />} />
      {/* 全服操作不在侧栏之外另开权限判断：侧栏只决定「看不看得见入口」，
          直接敲 URL 的人也要被挡，判据和侧栏用的是同一份 rolePermissions。
          页内两个页签各有各的权限（公告 server.broadcast / 全服邮件 server.mail_all），
          这里判的是「能不能进这个页面」，任一条命中即可。 */}
      <Route path="broadcast" element={canAny(me.data ?? null, ['server.broadcast', 'server.mail_all']) ? <Broadcast /> : <Navigate to="/" replace />} />
      <Route path="configs" element={subGM ? <Navigate to="/players" replace /> : <Configs />} />
      <Route path="sub-gms" element={subGM ? <Navigate to="/players" replace /> : <SubGMs />} />
      <Route path="operations" element={subGM ? <Navigate to="/players" replace /> : <Operations />} />
      <Route path="audit" element={subGM ? <Navigate to="/players" replace /> : <Audit />} />
    </Route>
    <Route path="*" element={<Navigate to={authenticated ? '/' : '/login'} replace />} />
  </Routes>
}
