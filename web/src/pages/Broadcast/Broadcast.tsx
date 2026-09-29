import { useMutation, useQuery } from '@tanstack/react-query'
import { AlertTriangle, Mail, Megaphone, RefreshCw, Send, X } from 'lucide-react'
import { FormEvent, useEffect, useMemo, useState } from 'react'
import { api, newIdempotencyKey } from '../../api/client'
import { Button, Card, Field, LoadingState, PageHeader, StatusPill } from '../../components/Ui'
import { ItemPicker, SelectedItem, itemSelectionError, itemSelectionPayload } from '../../components/ItemPicker'
import { can, useAuth } from '../../layouts/Shell'

// 与游戏服 gm_control_broadcast.go 的上限保持一致。这里只是即时反馈，
// 真正的判据在游戏服 —— 超限的内容会被它拒绝，命令不会下发给任何角色。
const maxLines = 10
const maxLineRunes = 120
// 标题与正文按字节计，和单人发信 / gmMailAll 一样（db 列宽就是字节）。
const maxTitleBytes = 191
const maxContentBytes = 4000

type Mode = 'announce' | 'mail'

const modeMeta: Record<Mode, { label: string; permission: string; icon: typeof Megaphone }> = {
  announce: { label: '全服公告', permission: 'server.broadcast', icon: Megaphone },
  mail: { label: '全服邮件', permission: 'server.mail_all', icon: Mail },
}
const modes: Mode[] = ['announce', 'mail']

type Outcome = { tone: 'success' | 'info' | 'danger'; text: string }
// 命令结果的两种来路：同步返回（游戏服 8 秒内确认）和轮询查回（查库拿 resultJson）。
// 归一到同一个形状，展示逻辑只写一份。
type RemoteResult = { status: string; errorCode?: string; message?: string; data?: Record<string, unknown> }

function runeLength(value: string) {
  // 与 Go 的 utf8.RuneCountInString 对齐：按码点计数，不按 UTF-16 码元。
  // 「😀」在 .length 里是 2，在这里是 1，和服务端口径一致。
  return [...value].length
}

// 和 Go 的 len(string) 对齐：按 UTF-8 字节计数。
function byteLength(value: string) {
  return new TextEncoder().encode(value).length
}

function objectValue(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function parseCommandResult(resultJson?: string): Record<string, unknown> {
  if (!resultJson) return {}
  try {
    return objectValue((JSON.parse(resultJson) as { data?: unknown } | null)?.data)
  } catch {
    return {}
  }
}

function formatCount(value: unknown) {
  return typeof value === 'number' ? new Intl.NumberFormat('zh-CN').format(value) : String(value ?? '—')
}

function formatAudience(value?: string) {
  if (value === undefined || value === '') return '—'
  const number = Number(value)
  return Number.isFinite(number) ? new Intl.NumberFormat('zh-CN').format(number) : value
}

// 失败原因可能来自三处：结构化结果里的 message、命令记录里的 message、以及 errorCode。
// 依次取第一个非空的，否则运营只看到「请求失败」，不知道该改什么。
function rejectReason(result: RemoteResult, fallback: string) {
  return (typeof result.data?.message === 'string' && result.data.message) || result.message || result.errorCode || fallback
}

function outcomeOf(result: RemoteResult, mode: Mode): Outcome {
  const data = result.data || {}
  if (result.status === 'completed') {
    if (mode === 'mail') {
      return { tone: 'success', text: `全服邮件已投递：共 ${formatCount(data.total)} 个角色，其中 ${formatCount(data.online)} 个在线已即时收到，其余 ${formatCount(data.offline)} 个下次登录可见；附件 ${formatCount(data.items)} 项，角色领取后到账。` }
    }
    return { tone: 'success', text: `公告已下发：${formatCount(data.online)} 名在线角色中，${formatCount(data.sent)} 名收到。` }
  }
  if (result.status === 'rejected') {
    const reason = rejectReason(result, mode === 'mail' ? '游戏服拒绝了这封全服邮件' : '游戏服拒绝了这条公告')
    return { tone: 'danger', text: `${mode === 'mail' ? '邮件未发出' : '公告未发出'}：${reason}` }
  }
  return { tone: 'info', text: `命令状态：${result.status}` }
}

function attachmentLabel(item: SelectedItem) {
  return `${item.item.name} × ${Number(item.count).toLocaleString('zh-CN')}`
}

export function Broadcast() {
  const session = useAuth()
  // 两个页签各有一份权限。只授了其中一项的角色只看到对应的页签，而不是看到一个
  // 点不动的灰按钮 —— 他要找的是自己能做的事，不是自己不能做的事。
  const allowed = useMemo(() => modes.filter(id => can(session, modeMeta[id].permission)), [session])
  const [mode, setMode] = useState<Mode>(allowed[0] ?? 'announce')

  const [text, setText] = useState('')
  const [center, setCenter] = useState(false)
  const [title, setTitle] = useState('')
  const [content, setContent] = useState('')
  const [items, setItems] = useState<SelectedItem[]>([])
  const [acknowledged, setAcknowledged] = useState(false)

  const [confirming, setConfirming] = useState(false)
  // 幂等键只在打开确认框的那一刻生成一次，之后重试一直用它。
  const [pinnedKey, setPinnedKey] = useState('')
  const [error, setError] = useState('')
  // 待确认的命令连同它属于哪个页签一起记下来：轮询回来时页签可能已经被切走，
  // 用当时那个模式渲染结果文案才不会把邮件的结果说成公告的。
  const [pending, setPending] = useState<{ id: string; mode: Mode } | null>(null)
  const [outcome, setOutcome] = useState<Outcome | null>(null)

  const audience = useQuery({ queryKey: ['broadcast-audience'], queryFn: api.broadcastAudience, staleTime: 15_000 })
  const lines = useMemo(() => text.split('\n').map(line => line.trim()).filter(line => line !== ''), [text])
  const longest = lines.reduce((max, line) => Math.max(max, runeLength(line)), 0)
  const problem = lines.length === 0 ? '请填写公告内容'
    : lines.length > maxLines ? `公告最多 ${maxLines} 行，当前 ${lines.length} 行`
      : longest > maxLineRunes ? `单行最多 ${maxLineRunes} 个字符，当前最长的一行有 ${longest} 个`
        : ''
  const mailProblem = !title.trim() ? '请填写邮件标题'
    : byteLength(title) > maxTitleBytes ? `邮件标题最多 ${maxTitleBytes} 字节（一个中文字约 3 字节），请缩短后重试`
      : !content.trim() ? '请填写邮件正文'
        : byteLength(content) > maxContentBytes ? `邮件正文最多 ${maxContentBytes} 字节（一个中文字约 3 字节），请精简后重试`
          : itemSelectionError(items, false)
  const activeProblem = mode === 'mail' ? mailProblem : problem

  const clearForm = (target: Mode) => {
    if (target === 'mail') { setTitle(''); setContent(''); setItems([]) } else { setText('') }
    setAcknowledged(false)
  }

  // 两个写操作共用后半段：确认框关闭、pending 记录、结果归一。只有请求本身和
  // 成功后清哪些字段不一样。
  const settle = (target: Mode, value: Record<string, unknown>) => {
    const status = String(value.status ?? '')
    setConfirming(false)
    // 游戏服没在等待窗口内确认：先如实说「还在等」，再靠轮询把最终结果追回来。
    // 不能在这里显示成功 —— 那会让运营以为几百个角色都收到了。
    if (status === 'pending' || status === 'accepted') {
      setPending({ id: String(value.requestId ?? pinnedKey), mode: target })
      setOutcome({ tone: 'info', text: target === 'mail' ? '全服邮件已交付游戏服，正在等待执行结果…' : '公告已交付游戏服，正在等待执行结果…' })
      return
    }
    setOutcome(outcomeOf({ status, errorCode: value.errorCode as string | undefined, message: value.message as string | undefined, data: objectValue(value.data) }, target))
    if (status === 'completed') clearForm(target)
  }

  const announce = useMutation({
    mutationFn: () => api.announce(lines, center, pinnedKey),
    onSuccess: value => settle('announce', value),
    onError: err => {
      // 确认框保持打开：键没变，改完网络问题再点一次「确认发送」不会发出第二条公告。
      setError(err instanceof Error ? err.message : '公告发送失败')
    },
  })
  const mailAll = useMutation({
    mutationFn: () => api.mailAll(title, content, itemSelectionPayload(items), pinnedKey),
    onSuccess: value => settle('mail', value),
    onError: err => setError(err instanceof Error ? err.message : '全服邮件发送失败'),
  })
  const busy = announce.isPending || mailAll.isPending

  const poll = useQuery({
    queryKey: ['command', pending?.id ?? ''],
    queryFn: () => api.command(pending!.id),
    enabled: pending !== null,
    refetchInterval: query => {
      const status = query.state.data?.status
      return !status || status === 'pending' || status === 'accepted' ? 1500 : false
    },
  })
  useEffect(() => {
    const record = poll.data
    if (!pending || !record) return
    if (record.status === 'pending' || record.status === 'accepted') return
    const target = pending.mode
    setPending(null)
    setOutcome(outcomeOf({ status: record.status, errorCode: record.errorCode, data: parseCommandResult(record.resultJson) }, target))
    if (record.status === 'completed') clearForm(target)
  }, [poll.data, pending])

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (busy || pending !== null) return
    if (activeProblem) { setError(activeProblem); return }
    setError('')
    setOutcome(null)
    setAcknowledged(false)
    setPinnedKey(newIdempotencyKey())
    setConfirming(true)
    // 受众数字是决策依据，弹确认框前重新取一次，别让运营对着十几分钟前的缓存数点确认。
    void audience.refetch()
  }

  const switchMode = (next: Mode) => {
    if (busy || pending !== null || next === mode) return
    setMode(next); setError(''); setOutcome(null); setConfirming(false)
  }

  const closeConfirm = () => { if (!busy) { setConfirming(false); setError('') } }
  const online = audience.data?.online
  const onlineUnknown = online === '-1'
  const total = audience.data?.totalPlayers
  // 收件范围是这封邮件唯一的防线：读不到总数就不让点确认。公告发错只是多刷一条
  // 聊天，全服邮件发错是往几百个邮件箱里塞东西，看不到收件人数就不该放行。
  const totalUnknown = audience.isError || total === undefined || total === ''
  const Meta = modeMeta[mode]
  const ModeIcon = Meta.icon

  if (allowed.length === 0) return <>
    <PageHeader eyebrow="OPERATIONS / SERVER" title="全服操作" description="向全服发送系统公告，或给全部角色投递同一封带附件的邮件" />
    <Card className="card-pad"><div className="notice danger" role="alert">当前账号没有全服操作权限。全服公告需要 server.broadcast，全服邮件需要 server.mail_all，请联系超级管理员授予。</div></Card>
  </>

  return <>
    <PageHeader eyebrow="OPERATIONS / SERVER" title="全服操作" description="向全服发送系统公告，或给全部角色投递同一封带附件的邮件" />
    <div className="tabs" role="tablist" aria-label="全服操作类型" style={{ marginBottom: 16 }}>
      {allowed.map(id => {
        const Icon = modeMeta[id].icon
        return <button key={id} type="button" role="tab" aria-selected={mode === id} disabled={busy || pending !== null} className={mode === id ? 'tab active' : 'tab'} onClick={() => switchMode(id)}>
          <Icon size={14} style={{ verticalAlign: 'middle', marginRight: 6 }} />{modeMeta[id].label}
        </button>
      })}
    </div>
    <div className="grid grid-2">
      <Card className="card-pad">
        <div className="section-title" style={{ padding: 0, border: 0, marginBottom: 16 }}><h2><ModeIcon size={15} style={{ verticalAlign: 'middle', marginRight: 7 }} />{Meta.label}</h2><span>{mode === 'mail' ? '在线与离线角色都会收到' : `每行一条，最多 ${maxLines} 行`}</span></div>
        <form onSubmit={submit}>
          {mode === 'announce' ? <>
            <Field label="公告内容" hint={`有效行 ${lines.length}/${maxLines} · 最长一行 ${longest}/${maxLineRunes} 个字符；空行会被忽略，不占行数`}>
              <textarea className="textarea" value={text} disabled={busy} onChange={event => setText(event.target.value)} placeholder={'停机维护通知\n预计 22:00 恢复服务'} />
            </Field>
            <Field label="显示方式" hint="中央横幅会占据屏幕正中间，用于停服等需要立刻看到的内容；普通消息走系统频道即可">
              <select className="select" value={center ? '1' : '0'} disabled={busy} onChange={event => setCenter(event.target.value === '1')}>
                <option value="0">仅系统频道</option>
                <option value="1">系统频道 + 屏幕中央横幅</option>
              </select>
            </Field>
            <div className="notice" style={{ marginTop: 13 }}>公告只发给<strong>当前在线</strong>的角色，不会补发给之后登录的人。离线玩家收不到，这是公告本身的语义。</div>
          </> : <>
            <Field label="邮件标题" hint={`最多 ${maxTitleBytes} 字节（一个中文字约 3 字节）`}>
              <input className="input" value={title} disabled={busy} onChange={event => setTitle(event.target.value)} placeholder="停服补偿" />
            </Field>
            <Field label="邮件正文" hint={`最多 ${maxContentBytes} 字节；发件人固定为「系统」`}>
              <textarea className="textarea" style={{ minHeight: 110 }} value={content} disabled={busy} onChange={event => setContent(event.target.value)} placeholder="感谢各位冒险者的支持，补偿物品请到邮件中领取。" />
            </Field>
            <ItemPicker items={items} onChange={setItems} disabled={busy} optional />
            <div className="notice" style={{ marginTop: 13 }}>邮件发给<strong>全部角色</strong>，含离线角色 —— 他们下次登录时会看到。附件不会直接进背包，需要角色自己<strong>领取</strong>。</div>
          </>}
          {activeProblem && (mode === 'mail' || lines.length) ? <div className="notice danger" role="alert" style={{ marginTop: 13 }}>{activeProblem}</div> : null}
          {error && !confirming ? <div className="notice danger" role="alert" style={{ marginTop: 13 }}>{error}</div> : null}
          {
            // 等待期间按钮置灰：轮询结果没回来之前不能再发下一条，否则运营会连点。
            <div className="modal-actions" style={{ marginTop: 18 }}>
              <Button type="submit" loading={busy} disabled={pending !== null}>
                <Send size={15} />{mode === 'mail' ? '发送全服邮件' : '发送公告'}
              </Button>
            </div>
          }
        </form>
      </Card>
      <div className="grid" style={{ gap: 16, alignContent: 'start' }}>
        <Card className="card-pad">
          <div className="section-title" style={{ padding: 0, border: 0, marginBottom: 16 }}><h2>受众预览</h2><StatusPill status={audience.isError ? 'error' : 'ok'}>{audience.isError ? '读取失败' : '实时'}</StatusPill></div>
          {audience.isLoading ? <LoadingState /> : audience.isError ? <div className="notice danger">受众读取失败，不影响发送，但请先确认在线人数。</div> : <div className="kv-list">
            <div className="kv"><span>在线角色（公告实际收件人）</span><span>{onlineUnknown ? '未知（Redis 不可用）' : formatAudience(online)}</span></div>
            <div className="kv"><span>全服角色总数</span><span>{formatAudience(total)}</span></div>
          </div>}
          <div style={{ marginTop: 16 }}><Button type="button" variant="secondary" disabled={audience.isFetching} onClick={() => void audience.refetch()}><RefreshCw size={15} />刷新受众</Button></div>
        </Card>
        {outcome ? <Card className="card-pad"><div className={`notice ${outcome.tone}`} role="status" aria-live="polite">{outcome.text}</div>{pending ? <div style={{ marginTop: 13 }}><LoadingState /></div> : null}</Card> : null}
      </div>
    </div>
    {confirming ? <div className="modal-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) closeConfirm() }}>
      <div role="dialog" aria-modal="true" aria-labelledby="broadcast-confirm-title" className="modal">
        <div className="modal-header"><h2 id="broadcast-confirm-title"><AlertTriangle size={17} style={{ verticalAlign: 'middle', marginRight: 7 }} />确认发送{Meta.label}</h2><button type="button" className="icon-btn" onClick={closeConfirm} disabled={busy} aria-label="关闭"><X size={18} /></button></div>
        {mode === 'announce' ? <>
          <p>公告会立刻下发给<strong>当前所有在线角色</strong>，发出后无法撤回。请核对下面的内容。</p>
          <div className="kv-list" style={{ margin: '14px 0' }}>
            <div className="kv"><span>收件人</span><span>{onlineUnknown ? '在线数未知（Redis 不可用）' : `${formatAudience(online)} 名在线角色`}</span></div>
            <div className="kv"><span>显示方式</span><span>{center ? '系统频道 + 中央横幅' : '仅系统频道'}</span></div>
            <div className="kv"><span>行数</span><span>{lines.length} 行</span></div>
          </div>
          <div className="notice" style={{ marginBottom: 13 }}>{lines.map((line, index) => <div key={index} className="mono">{line}</div>)}</div>
          {onlineUnknown ? <div className="notice danger" role="alert" style={{ marginBottom: 13 }}>在线数读取不到。这不影响发送，但你看不到这条公告实际会发给多少人。</div> : null}
        </> : <>
          <p>这封邮件会写进<strong>每一个角色</strong>的邮件箱，含离线的；发出后无法撤回，也不能批量删除。请逐字核对标题与正文。</p>
          <div className="kv-list" style={{ margin: '14px 0' }}>
            <div className="kv"><span>收件人</span><span>{totalUnknown ? '角色总数读取失败' : `全部 ${formatAudience(total)} 个角色（在线 ${onlineUnknown ? '未知' : formatAudience(online)}）`}</span></div>
            <div className="kv"><span>附件</span><span>{items.length ? `${items.length} 种` : '无'}</span></div>
            <div className="kv"><span>发件人</span><span>系统</span></div>
          </div>
          <div className="notice" style={{ marginBottom: 13 }}>
            <div><strong>标题：</strong>{title.trim()}</div>
            <div style={{ whiteSpace: 'pre-wrap', marginTop: 6 }}><strong>正文：</strong>{content}</div>
          </div>
          {items.length ? <div className="notice" style={{ marginBottom: 13, maxHeight: 170, overflow: 'auto' }}>{items.map(item => <div key={item.item.id} className="mono">{attachmentLabel(item)}</div>)}</div> : null}
          {totalUnknown ? <div className="notice danger" role="alert" style={{ marginBottom: 13 }}>角色总数读取失败，无法确认这次要发给多少个角色。请先「返回修改」→ 刷新受众，再重新确认。</div> : null}
          <label className="notice" style={{ marginBottom: 13, display: 'flex', gap: 9, alignItems: 'flex-start', cursor: totalUnknown ? 'not-allowed' : 'pointer' }}>
            <input type="checkbox" checked={acknowledged} disabled={totalUnknown || busy} onChange={event => setAcknowledged(event.target.checked)} style={{ marginTop: 2 }} />
            <span>{totalUnknown ? '读取不到角色总数，无法确认发放范围' : `我确认向全部 ${formatAudience(total)} 个角色发放这封邮件，并已核对标题、正文与附件`}</span>
          </label>
        </>}
        {error ? <div className="notice danger" role="alert" style={{ marginBottom: 13 }}>{error}</div> : null}
        <div className="modal-actions">
          <Button type="button" variant="secondary" disabled={busy} onClick={closeConfirm}>返回修改</Button>
          <Button type="button" loading={busy} disabled={mode === 'mail' && (!acknowledged || totalUnknown)} onClick={() => (mode === 'mail' ? mailAll.mutate() : announce.mutate())}><Send size={15} />确认发送</Button>
        </div>
      </div>
    </div> : null}
  </>
}
