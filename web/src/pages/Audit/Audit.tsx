import { useQuery } from '@tanstack/react-query'
import { ClipboardList, ScrollText, Search, TerminalSquare } from 'lucide-react'
import { useState } from 'react'
import { api, AuditRecord, CommandRecord } from '../../api/client'
import { Card, EmptyState, ErrorState, LoadingState, PageHeader, StatusPill } from '../../components/Ui'

function time(value: string) {
  const date = new Date(Number(value) * 1000)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString('zh-CN', { hour12: false })
}

export function Audit() {
  const [tab, setTab] = useState<'audit' | 'commands'>('audit')
  const [filter, setFilter] = useState('')
  const audit = useQuery({ queryKey: ['audit'], queryFn: () => api.audit() })
  const commands = useQuery({ queryKey: ['commands'], queryFn: () => api.commands() })
  const current = tab === 'audit' ? audit : commands
  const needle = filter.toLowerCase()
  const auditItems = (audit.data?.items || []).filter(item => !needle || item.action.toLowerCase().includes(needle))
  const commandItems = (commands.data?.items || []).filter(item => !needle || item.action.toLowerCase().includes(needle) || item.requestId.includes(filter))

  return <>
    <PageHeader eyebrow="CONTROL PLANE / TRACE" title="审计与命令" description="每次副作用操作都可追踪，命令结果保留请求 ID" />
    <div className="toolbar"><div className="search"><Search size={16} /><input className="input" value={filter} onChange={e => setFilter(e.target.value)} placeholder={tab === 'audit' ? '搜索动作' : '搜索动作或 requestId'} /></div></div>
    <div className="tabs" style={{ marginBottom: 16 }}><button className={tab === 'audit' ? 'tab active' : 'tab'} onClick={() => setTab('audit')}><ClipboardList size={14} />审计日志</button><button className={tab === 'commands' ? 'tab active' : 'tab'} onClick={() => setTab('commands')}><TerminalSquare size={14} />控制命令</button></div>
    <Card><div className="section-title"><h2><ScrollText size={15} style={{ verticalAlign: 'middle', marginRight: 7 }} />{tab === 'audit' ? '操作审计' : '命令记录'}</h2><span>{current.data ? `${current.data.items.length} 条记录` : '读取中'}</span></div>
      {current.isLoading ? <LoadingState /> : current.isError ? <ErrorState message={current.error instanceof Error ? current.error.message : undefined} onRetry={() => current.refetch()} /> : tab === 'audit' ? <AuditTable items={auditItems} /> : <CommandTable items={commandItems} />}
      <div className="pagination"><span>游标分页 · 默认展示最近 50 条</span><span>敏感字段已由服务端裁剪</span></div>
    </Card>
  </>
}

function AuditTable({ items }: { items: AuditRecord[] }) {
  if (!items.length) return <EmptyState />
  return <div className="table-wrap"><table className="data-table"><thead><tr><th>时间</th><th>动作</th><th>目标</th><th>结果</th><th>请求 ID</th></tr></thead><tbody>{items.map(item => <tr key={item.id}><td className="mono muted">{time(item.createdAt)}</td><td className="mono">{item.action}</td><td>{item.targetType} <span className="muted mono">{item.targetId}</span></td><td><StatusPill status={item.result}>{item.result}</StatusPill></td><td className="mono muted" style={{ fontSize: 10 }}>{item.requestId.slice(0, 16)}…</td></tr>)}</tbody></table></div>
}

function CommandTable({ items }: { items: CommandRecord[] }) {
  if (!items.length) return <EmptyState />
  return <div className="table-wrap"><table className="data-table"><thead><tr><th>创建时间</th><th>动作</th><th>目标角色</th><th>状态</th><th>请求 ID</th></tr></thead><tbody>{items.map(item => <tr key={item.requestId}><td className="mono muted">{time(item.createdAt)}</td><td className="mono">{item.action}</td><td className="mono">{item.targetId}</td><td><StatusPill status={item.status}>{item.status}</StatusPill></td><td className="mono muted" style={{ fontSize: 10 }}>{item.requestId}</td></tr>)}</tbody></table></div>
}
