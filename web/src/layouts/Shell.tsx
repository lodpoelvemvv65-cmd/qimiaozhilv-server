import { useQueryClient } from '@tanstack/react-query'
import { Activity, Boxes, ChevronRight, LayoutDashboard, LogOut, Megaphone, Menu, ScrollText, ServerCog, ShieldCheck, Users, X } from 'lucide-react'
import { createContext, ReactNode, useContext, useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { api, clearCsrfToken, Session } from '../api/client'
import { Button } from '../components/Ui'

type NavItem = { to: string; label: string; icon: typeof LayoutDashboard; exact?: boolean; permission?: string; permissions?: string[] }

const nav: NavItem[] = [
  { to: '/', label: '运行总览', icon: LayoutDashboard, exact: true, permission: 'dashboard.read' },
  { to: '/players', label: '角色管理', icon: Users, permission: 'players.read' },
  // 全服操作页有两个页签（公告 / 全服邮件），权限也是分开的两条。任一条命中就要
  // 显示入口 —— 只授了全服邮件的运营进来只看得到那一个页签，这是他该看到的结果。
  { to: '/broadcast', label: '全服操作', icon: Megaphone, permissions: ['server.broadcast', 'server.mail_all'] },
  { to: '/operations', label: '运营数据', icon: Activity, permission: 'families.read' },
  { to: '/configs', label: '配置中心', icon: ServerCog, permission: 'configs.read' },
  { to: '/sub-gms', label: '子 GM', icon: Users, permission: 'subgm.manage' },
  { to: '/audit', label: '审计与命令', icon: ScrollText, permission: 'audit.read' },
]

const rolePermissions: Record<string, string[]> = {
  superadmin: ['*'],
  operator: ['dashboard.read', 'accounts.read', 'players.read', 'families.read', 'consignments.read', 'bosses.read', 'audit.read', 'commands.read', 'players.kick', 'players.reload', 'players.mail', 'players.items', 'players.currency', 'players.level', 'players.pet', 'players.skills', 'players.starsoul', 'players.equip', 'players.job', 'players.activity', 'players.stats', 'configs.read', 'configs.publish', 'server.broadcast', 'server.mail_all'],
  support: ['dashboard.read', 'players.read', 'players.kick', 'players.reload', 'players.mail', 'commands.read'],
  'config-editor': ['dashboard.read', 'configs.read', 'configs.publish', 'audit.read'],
  auditor: ['dashboard.read', 'accounts.read', 'players.read', 'families.read', 'consignments.read', 'bosses.read', 'audit.read', 'commands.read', 'configs.read'],
  readonly: ['dashboard.read', 'players.read', 'accounts.read'],
  subgm: ['players.read', 'subgm.bind', 'players.kick', 'players.reload', 'players.mail', 'players.items', 'players.currency', 'players.level', 'players.pet', 'players.skills', 'players.starsoul', 'players.equip', 'players.job', 'players.activity'],
}
export const AuthContext = createContext<Session | null>(null)
export function useAuth() { return useContext(AuthContext) }
export function can(session: Session | null, permission: string) { return !!session?.roles.some(role => rolePermissions[role]?.includes('*') || rolePermissions[role]?.includes(permission)) }
// 任一条命中即通过。用在「入口」这一层：入口代表这里有你的事可做，具体能做哪几件
// 由页面内部按更细的权限再判一次。
export function canAny(session: Session | null, permissions: string[]) { return permissions.some(permission => can(session, permission)) }

export function Shell({ session }: { session: Session }) {
  const [open, setOpen] = useState(false)
  const navigate = useNavigate(); const location = useLocation(); const queryClient = useQueryClient()
  const logout = async () => {
    try { await api.logout() } finally {
      clearCsrfToken()
      // Keep the active auth query mounted with an explicit empty value. A
      // cache clear alone can leave its observer holding the old session for
      // one render, which immediately redirects /login back to the console.
      queryClient.setQueryData(['me'], null)
      queryClient.removeQueries({ predicate: query => query.queryKey[0] !== 'me' })
      navigate('/login', { replace: true })
    }
  }
  const current = nav.find(item => item.exact ? location.pathname === item.to : location.pathname.startsWith(item.to))
  return <AuthContext.Provider value={session}><div className="app-shell">
    <aside className={`sidebar ${open ? 'open' : ''}`}>
      <div className="brand"><div className="brand-mark"><Boxes size={19} /></div><div><strong>MHQ</strong><span>GM CONSOLE</span></div><button className="icon-btn mobile-only" onClick={() => setOpen(false)} aria-label="关闭导航"><X size={19} /></button></div>
      <div className="server-chip"><span className="pulse" /><div><b>LOCAL CONTROL</b><small>127.0.0.1:7756</small></div></div>
      <nav aria-label="主导航">{nav.filter(item => item.permissions ? canAny(session, item.permissions) : can(session, item.permission ?? '')).map(item => { const Icon = item.icon; return <NavLink key={item.to} to={item.to} end={item.exact} onClick={() => setOpen(false)} className={({ isActive }) => isActive ? 'nav-link active' : 'nav-link'}><Icon size={18} /><span>{item.label}</span><ChevronRight size={15} className="nav-arrow" /></NavLink> })}</nav>
      <div className="sidebar-footer"><div className="security-note"><ShieldCheck size={16} /><span>受控管理通道<br /><small>审计记录已启用</small></span></div><Button type="button" variant="ghost" className="logout" onClick={logout}><LogOut size={16} />退出登录</Button></div>
    </aside>
    {open ? <button className="sidebar-scrim" onClick={() => setOpen(false)} aria-label="关闭导航" /> : null}
    <main className="main-area">
      <header className="topbar"><button className="icon-btn mobile-only" onClick={() => setOpen(true)} aria-label="打开导航"><Menu size={20} /></button><div className="breadcrumbs"><span>控制台</span><ChevronRight size={14} /><strong>{current?.label || '页面'}</strong></div><div className="topbar-actions"><span className="live-dot" /> <span className="live-label">服务在线</span><div className="profile"><div className="avatar">{(session.displayName || session.username).slice(0, 1).toUpperCase()}</div><div><b>{session.displayName || session.username}</b><small>{session.roles[0] || 'operator'}</small></div></div></div></header>
      <div className="content"><Outlet /></div>
    </main>
  </div></AuthContext.Provider>
}

export function InlineNotice({ children, tone = 'info' }: { children: ReactNode; tone?: 'info' | 'success' | 'danger' }) { return <div className={`notice ${tone}`}>{children}</div> }
