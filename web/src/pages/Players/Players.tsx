import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Search, SlidersHorizontal, Wifi, WifiOff } from 'lucide-react'
import { FormEvent, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../../api/client'
import { Button, Card, EmptyState, ErrorState, Field, LoadingState, PageHeader, StatusPill } from '../../components/Ui'
import { useAuth } from '../../layouts/Shell'
import { formatCoin, formatCoinCopper } from '../../lib/money'

export function Players() {
  const [query, setQuery] = useState('')
  const [search, setSearch] = useState('')
  const [online, setOnline] = useState('')
  const [account, setAccount] = useState('')
  const [accountPassword, setAccountPassword] = useState('')
  const [bindError, setBindError] = useState('')
  const [bindMessage, setBindMessage] = useState('')
  const session = useAuth()
  const subGM = session?.roles.includes('subgm') === true
  const client = useQueryClient()
  const players = useQuery({ queryKey: ['players', search, online], queryFn: () => api.players({ query: search, online }) })
  const bindings = useQuery({ queryKey: ['subgm-bindings'], queryFn: api.subGMBindings, enabled: subGM })
  const bind = useMutation({
    mutationFn: () => api.bindSubGMAccount(account.trim(), accountPassword),
    onSuccess: () => {
      setAccount('')
      setAccountPassword('')
      setBindError('')
      setBindMessage('游戏账号校验成功，已绑定。')
      void client.invalidateQueries({ queryKey: ['subgm-bindings'] })
      void client.invalidateQueries({ queryKey: ['players'] })
    },
    onError: error => { setBindMessage(''); setBindError(error instanceof Error ? error.message : '游戏账号校验失败') },
  })
  const submitBinding = (event: FormEvent) => {
    event.preventDefault()
    if (bind.isPending) return
    setBindError('')
    setBindMessage('')
    const username = account.trim()
    if (!username || !accountPassword) { setBindError('请输入游戏账号和密码'); return }
    if ((bindings.data?.items.length ?? 0) >= 5) { setBindError('最多只能绑定 5 个游戏账号'); return }
    bind.mutate()
  }
  return <>
    <PageHeader eyebrow={subGM ? 'SUB GM / PLAYERS' : 'OPERATIONS / PLAYERS'} title="角色管理" description={subGM ? '仅显示已绑定游戏账号的角色数据，可执行角色管理操作' : '搜索角色、查看状态，并进入受控操作面板'} />
    {subGM ? <Card style={{ marginBottom: 16 }}>
      <div className="section-title"><h2>绑定游戏账号</h2><span>{bindings.data?.items.length ?? 0} / 5</span></div>
      <form className="card-pad" onSubmit={submitBinding}>
        <div className="field-row">
          <Field label="游戏账号"><input className="input" value={account} onChange={event => setAccount(event.target.value)} autoComplete="section-game username" disabled={bind.isPending} required /></Field>
          <Field label="游戏账号密码"><input className="input" type="password" value={accountPassword} onChange={event => setAccountPassword(event.target.value)} autoComplete="section-game current-password" disabled={bind.isPending} required /></Field>
        </div>
        {bindError ? <div className="notice danger" role="alert" style={{ marginTop: 13 }}>{bindError}</div> : null}
        {bindMessage ? <div className="notice success" role="status" style={{ marginTop: 13 }}>{bindMessage}</div> : null}
        <Button type="submit" loading={bind.isPending} disabled={bind.isPending || (bindings.data?.items.length ?? 0) >= 5} style={{ marginTop: 15 }}>校验并绑定</Button>
        <div className="muted" style={{ marginTop: 12, fontSize: 12 }}>绑定会校验游戏账号密码；绑定后只能由主 GM 解绑，子 GM 不能自行解除。</div>
      </form>
      {bindings.data?.items.length ? <div className="table-wrap" style={{ borderTop: '1px solid rgba(71,85,105,.45)' }}><table className="data-table"><thead><tr><th>已绑定账号</th><th>角色数量</th></tr></thead><tbody>{bindings.data.items.map(item => <tr key={item.accountId}><td className="mono">{item.account}</td><td>{item.playerCount}</td></tr>)}</tbody></table></div> : null}
    </Card> : null}
    <div className="toolbar"><div className="search"><Search size={16} /><input className="input" value={query} onChange={event => setQuery(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') setSearch(query) }} placeholder="输入角色名或账号，按 Enter 搜索" /></div><select className="select" style={{ width: 140 }} value={online} onChange={event => setOnline(event.target.value)} aria-label="在线筛选"><option value="">全部状态</option><option value="true">仅在线</option><option value="false">仅离线</option></select><Button variant="secondary" onClick={() => setSearch(query)}><SlidersHorizontal size={15} />查询</Button></div>
    <Card><div className="section-title"><h2>角色列表</h2><span>{players.data ? `${players.data.items.length} 条结果` : '读取中'}</span></div>{players.isLoading ? <LoadingState /> : players.isError ? <ErrorState message={players.error instanceof Error ? players.error.message : undefined} onRetry={() => players.refetch()} /> : players.data?.items.length === 0 ? <EmptyState message="没有匹配的角色" /> : <div className="table-wrap"><table className="data-table"><thead><tr><th>角色</th><th>账号</th><th>等级 / 转生</th><th>货币</th><th>地图</th><th>状态</th><th /></tr></thead><tbody>{players.data?.items.map(player => <tr key={player.id}><td><Link className="link" to={`/players/${player.id}`}><strong>{player.name || '未命名'}</strong></Link><div className="muted mono" style={{ fontSize: 10, marginTop: 4 }}>#{player.id}</div></td><td className="mono">{player.account}</td><td><span className="mono">Lv.{player.level}</span><span className="muted" style={{ marginLeft: 8 }}>T{player.trans}</span></td><td className="mono" title={`${formatCoinCopper(player.coin)} 铜`}>{formatCoin(player.coin)}</td><td className="mono">#{player.mapId}</td><td><StatusPill status={player.online ? 'online' : 'offline'}>{player.online ? <><Wifi size={11} />在线</> : <><WifiOff size={11} />离线</>}</StatusPill></td><td><Link className="btn btn-ghost" to={`/players/${player.id}`}>查看</Link></td></tr>)}</tbody></table></div>}<div className="pagination"><span>{subGM ? '仅显示已绑定账号' : '游标分页 · BIGINT 以字符串传输'}</span><span>{players.data?.nextCursor ? '还有更多结果' : '已到末尾'}</span></div></Card>
  </>
}
