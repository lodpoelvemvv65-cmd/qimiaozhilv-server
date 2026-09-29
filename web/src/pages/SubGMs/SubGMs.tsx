import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Link2Off, Plus, RefreshCw, Users } from 'lucide-react'
import { FormEvent, useState } from 'react'
import { api, SubGM } from '../../api/client'
import { Button, Card, ErrorState, Field, LoadingState, PageHeader, StatusPill } from '../../components/Ui'

function formatTime(value: string) {
  const date = new Date(Number(value) * 1000)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString('zh-CN', { hour12: false })
}

export function SubGMs() {
  const client = useQueryClient()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [expanded, setExpanded] = useState('')
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')
  const subGMs = useQuery({ queryKey: ['sub-gms'], queryFn: api.subGMs })
  const bindings = useQuery({ queryKey: ['sub-gm-bindings', expanded], queryFn: () => api.subGMBindingsFor(expanded), enabled: !!expanded })
  const create = useMutation({
    mutationFn: () => api.createSubGM(username.trim(), password, displayName.trim()),
    onSuccess: value => { setMessage(`子 GM ${value.username} 已创建，请把账号和密码交给使用者。`); setError(''); setUsername(''); setPassword(''); setDisplayName(''); void client.invalidateQueries({ queryKey: ['sub-gms'] }) },
    onError: value => setError(value instanceof Error ? value.message : '创建子 GM 失败'),
  })
  const unbind = useMutation({
    mutationFn: ({ id, accountId }: { id: string; accountId: string }) => api.unbindSubGM(id, accountId),
    onSuccess: () => { setMessage('绑定已由主 GM 解除。'); void client.invalidateQueries({ queryKey: ['sub-gms'] }); void client.invalidateQueries({ queryKey: ['sub-gm-bindings', expanded] }) },
    onError: value => setError(value instanceof Error ? value.message : '解绑失败'),
  })
  const submit = (event: FormEvent) => {
    event.preventDefault(); setError(''); setMessage('')
    if (!/^[A-Za-z0-9_.-]{3,64}$/.test(username.trim())) { setError('账号只能使用 3-64 位 ASCII 字母、数字、下划线、点或短横线'); return }
    if (password.length < 12) { setError('密码至少需要 12 位'); return }
    create.mutate()
  }
  return <>
    <PageHeader eyebrow="ACCESS / SUB GM" title="子 GM 管理" description="创建受限账号，并由主 GM 管理游戏账号绑定" action={<Button variant="secondary" onClick={() => subGMs.refetch()}><RefreshCw size={15} />刷新</Button>} />
    <div className="grid grid-2">
      <Card><div className="section-title"><h2><Plus size={15} style={{ verticalAlign: 'middle', marginRight: 6 }} />创建子 GM</h2><span>最多绑定 5 个游戏账号</span></div><form className="card-pad" onSubmit={submit}><Field label="子 GM 账号"><input className="input" value={username} onChange={event => setUsername(event.target.value)} autoComplete="off" required /></Field><Field label="初始密码"><input className="input" type="password" value={password} onChange={event => setPassword(event.target.value)} autoComplete="new-password" minLength={12} required /></Field><Field label="显示名称"><input className="input" value={displayName} onChange={event => setDisplayName(event.target.value)} placeholder="可选" /></Field>{error ? <div className="notice danger" role="alert" style={{ marginTop: 13 }}>{error}</div> : null}{message ? <div className="notice success" role="status" style={{ marginTop: 13 }}>{message}</div> : null}<Button type="submit" loading={create.isPending} style={{ marginTop: 16 }}><KeyRound size={15} />创建并生成账号</Button></form></Card>
      <Card><div className="section-title"><h2><Users size={15} style={{ verticalAlign: 'middle', marginRight: 6 }} />绑定规则</h2></div><div className="card-pad"><div className="notice">子 GM 登录后可以管理自己绑定账号下的角色。</div><p className="muted" style={{ lineHeight: 1.7, fontSize: 12 }}>子 GM 绑定时必须校验游戏账号密码；每个子 GM 最多绑定 5 个游戏账号，不能自行解绑。解绑只能由主 GM 在下方操作。</p></div></Card>
    </div>
    <Card style={{ marginTop: 16 }}><div className="section-title"><h2>子 GM 账号</h2><span>{subGMs.data?.items.length ?? 0} 个账号</span></div>{subGMs.isLoading ? <LoadingState /> : subGMs.isError ? <ErrorState message={subGMs.error instanceof Error ? subGMs.error.message : undefined} onRetry={() => subGMs.refetch()} /> : <div className="table-wrap"><table className="data-table"><thead><tr><th>账号</th><th>显示名称</th><th>状态</th><th>绑定数量</th><th>创建时间</th><th /></tr></thead><tbody>{subGMs.data?.items.map((item: SubGM) => <tr key={item.id}><td className="mono">{item.username}</td><td>{item.displayName}</td><td><StatusPill status={item.status ? 'up' : 'down'}>{item.status ? '启用' : '停用'}</StatusPill></td><td>{item.bindingCount} / 5</td><td className="mono muted">{formatTime(item.createdAt)}</td><td><Button variant="ghost" onClick={() => setExpanded(expanded === item.id ? '' : item.id)}>{expanded === item.id ? '收起绑定' : '查看绑定'}</Button></td></tr>)}</tbody></table></div>}{expanded ? <div className="card-pad" style={{ borderTop: '1px solid rgba(71,85,105,.45)' }}>{bindings.isLoading ? <LoadingState /> : bindings.data?.items.length ? <div className="table-wrap"><table className="data-table"><thead><tr><th>游戏账号</th><th>角色数量</th><th /></tr></thead><tbody>{bindings.data.items.map(binding => <tr key={binding.accountId}><td className="mono">{binding.account}</td><td>{binding.playerCount}</td><td><Button variant="danger" loading={unbind.isPending} onClick={() => unbind.mutate({ id: expanded, accountId: binding.accountId })}><Link2Off size={14} />主 GM 解绑</Button></td></tr>)}</tbody></table></div> : <div className="empty">暂无绑定账号</div>}</div> : null}</Card>
  </>
}
