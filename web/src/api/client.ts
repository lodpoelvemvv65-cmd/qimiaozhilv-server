export type ApiEnvelope<T> = { requestId: string; data: T | null; error: { code: string; message: string; details?: Record<string, unknown> } | null }

export type Session = { adminId: string; username: string; displayName: string; roles: string[]; expiresAt: string; csrfToken?: string }
export type Health = { status: string; startedAt: string; checkedAt: string; mysql: Dependency; redis: Dependency; game: Dependency }
export type Dependency = { status: string; latencyMs: string; detail?: string }
export type Dashboard = { accounts: string; players: string; online: string; recentLogins24h: string; configRevision: string; onlineSource: string; health: Health }
export type Player = { id: string; account: string; name: string; jobId: number; level: number; trans: number; familyId: string; mapId: number; lastLogin: string; coin: string; yuanBao: string; voucher: string; honor: string; pvpCurrency: string; storeCoin: string; online: boolean }
export type PlayerDetail = Player & { skinId: number; titleId: number; exp: string; energy: number; posX: number; posY: number; charPoint: number; skillPoint: number; attributes: Record<string, number>; autoBattle: number; storePages: number; familyContribute: number; personalContribute: number; sessionState: string }
export type Page<T> = { items: T[]; nextCursor: string }
export type CatalogItem = { id: number; name: string; category: string; itemType: number; source: string; level: number; quality: number; jobType: number; jobName: string; equipSlot: number; equipSlotName: string; description: string; delivery: string }
export type CatalogSkill = { id: number; name: string; jobType: number; jobName: string }
export type Config = { name: string; nodes: string; revision: string | null; contentYaml: string; content?: unknown }
export type ConfigRevision = { revision: string; publishedAt: string; publisher: string; note: string }
// resultJson 是游戏服回传的结构化结果原文（pending 时为空）。命令查询接口一直
// 有返回它，只是此前没人用；全服公告要靠它取回「实际发给了多少人」。
export type CommandRecord = { requestId: string; action: string; targetId: string; adminId: string; status: string; errorCode?: string; resultJson?: string; createdAt: string; finishedAt?: string }
export type AuditRecord = { id: string; requestId: string; adminId: string; action: string; targetType: string; targetId: string; reason: string; result: string; errorCode: string; durationMs: string; createdAt: string }
export type SubGM = { id: string; username: string; displayName: string; status: boolean; createdAt: string; bindingCount: number }
export type SubGMBinding = { accountId: string; account: string; playerCount: number }

// 手工装备候选池：GET /api/v1/equip-manual/options。所有条目都带中文名，前端不显示裸 id。
export type EquipManualRandomAttr = { id: number; key: number; keyName: string; value: number }
export type EquipManualAffixAttr = { key: number; keyName: string; value: number }
export type EquipManualAffix = { id: number; family: number; sixDimension: boolean; label: string; attrs: EquipManualAffixAttr[] }
export type EquipManualGem = { id: number; name: string; gemKey: number; gemKeyName: string; gemType: number; gemTypeName: string; gemLevel: number }
export type EquipManualEquipType = { type: number; name: string; allowedGemKeys: number[] }
export type EquipManualEquipRule = { itemId: number; name: string; type: number; typeName: string; maxHole: number; allowedGemKeys: number[]; allowedGemIds: number[]; canInlayGemTypes: number[] }
export type EquipManualOptions = {
  limits: { minStar: number; maxStar: number; maxStrength: number; minQuality: number; maxQuality: number; bonusCountByQuality: Record<string, number> }
  attributeNames: Record<string, string>
  equipTypes: EquipManualEquipType[]
  gemTypeNames: Record<string, string>
  gems: EquipManualGem[]
  randomAttrsByQuality: Record<string, EquipManualRandomAttr[]>
  affixes: EquipManualAffix[]
  affixPools: { byQuality: Record<string, number[]>; sixDimension: number[] }
  equipRules?: Record<string, EquipManualEquipRule>
}

// 全服操作的受众规模。online 为 "-1" 表示 Redis 不可用、在线数未知 —— 不能当成
// 「没人在线」，否则运营会以为公告发了也没用。
export type BroadcastAudience = { totalPlayers: string; online: string; onlineSource: string }

// 装备当前的随机属性 / 洗练词缀 / 宝石槽。三张子表按 location + slot_index 归并。
export type EquipManualStateRow = { location: unknown; slot_index: unknown; position: unknown; attribute_id?: unknown; affix_id?: unknown; gem_item_id?: unknown }

let csrfToken = ''
export function setCsrfToken(token: string) { csrfToken = token }
export function clearCsrfToken() { csrfToken = '' }
function csrfFromCookie() {
  const match = document.cookie.split(';').map(value => value.trim()).find(value => value.startsWith('mhq_gm_csrf='))
  return match ? decodeURIComponent(match.slice('mhq_gm_csrf='.length)) : ''
}

// randomUUID is unavailable in some HTTP pages and older embedded browsers.
// Keep idempotency keys unique there as well, so an unsupported convenience API
// cannot prevent an otherwise valid GM operation from being submitted.
function idempotencyKey() {
  const cryptoApi = globalThis.crypto
  if (typeof cryptoApi?.randomUUID === 'function') return cryptoApi.randomUUID()
  if (typeof cryptoApi?.getRandomValues === 'function') {
    const bytes = new Uint8Array(16)
    cryptoApi.getRandomValues(bytes)
    bytes[6] = (bytes[6] & 0x0f) | 0x40
    bytes[8] = (bytes[8] & 0x3f) | 0x80
    const hex = Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('')
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`
}

// 需要「先固定键、再让用户确认、失败可重试」的调用方自己生成键。playerCommand
// 那种一次点击一次调用的写操作没有这个需求，仍然每次现生成。
export const newIdempotencyKey = idempotencyKey

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (!csrfToken) csrfToken = csrfFromCookie()
  if (csrfToken && init.method && init.method !== 'GET') headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(path, { ...init, headers, credentials: 'include' })
  const envelope = await response.json() as ApiEnvelope<T>
  if (!response.ok || envelope.error) {
    // 游戏服拒绝 GM 指令时会把中文原因写在 data.message 里。正常情况下后端已经把它
    // 转成 error.message，这里再兜一层：gm-api 与游戏服是两个独立部署单元，版本没对齐
    // 时也不能退化成只有一个裸状态码。
    const fallbackMessage = (envelope.data as { message?: string } | null)?.message
    const error = new Error(
      envelope.error?.message || fallbackMessage || `请求失败（${response.status}）`,
    ) as Error & { code?: string; status?: number; requestId?: string; data?: unknown }
    error.code = envelope.error?.code
    error.status = response.status
    error.requestId = envelope.requestId
    // 失败时同样保留结构化结果：游戏服的 before/after 在 rejected 时也会回传。
    error.data = envelope.data
    throw error
  }
  return envelope.data as T
}

export const api = {
  login: async (username: string, password: string) => {
    const value = await request<Session>('/api/v1/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) })
    setCsrfToken(value.csrfToken || '')
    return value
  },
  logout: () => request<{ loggedOut: boolean }>('/api/v1/auth/logout', { method: 'POST', body: '{}' }),
  me: async () => {
    const value = await request<Session>('/api/v1/auth/me')
    setCsrfToken(value.csrfToken || csrfFromCookie())
    return value
  },
  health: () => request<Health>('/api/v1/health'),
  dashboard: () => request<Dashboard>('/api/v1/dashboard'),
  items: () => request<{ items: CatalogItem[]; total: number }>('/api/v1/items'),
  skills: () => request<{ items: CatalogSkill[]; total: number }>('/api/v1/skills'),
  equipManualOptions: (itemId?: string) => request<EquipManualOptions>(`/api/v1/equip-manual/options${itemId ? `?itemId=${encodeURIComponent(itemId)}` : ''}`),
  players: (params: { query?: string; online?: string; cursor?: string }) => request<Page<Player>>(`/api/v1/players?limit=50&query=${encodeURIComponent(params.query || '')}&online=${encodeURIComponent(params.online || '')}&cursor=${encodeURIComponent(params.cursor || '')}`),
  player: (id: string) => request<PlayerDetail>(`/api/v1/players/${encodeURIComponent(id)}`),
  playerResource: (id: string, resource: string) => request<{ resource: string; items: Record<string, unknown>[] }>(`/api/v1/players/${encodeURIComponent(id)}/${resource}`),
  families: (params: { query?: string; cursor?: string }) => request<Page<Record<string, unknown>>>(`/api/v1/families?limit=50&query=${encodeURIComponent(params.query || '')}&cursor=${encodeURIComponent(params.cursor || '')}`),
  consignments: (params: { cursor?: string }) => request<Page<Record<string, unknown>>>(`/api/v1/consignments?limit=50&cursor=${encodeURIComponent(params.cursor || '')}`),
  bosses: () => request<{ world: Record<string, unknown>[]; family: Record<string, unknown>[] }>('/api/v1/world-bosses'),
  commands: (cursor = '') => request<Page<CommandRecord>>(`/api/v1/commands?limit=50&cursor=${encodeURIComponent(cursor)}`),
  command: (id: string) => request<CommandRecord>(`/api/v1/commands/${encodeURIComponent(id)}`),
  audit: (cursor = '') => request<Page<AuditRecord>>(`/api/v1/audit?limit=50&cursor=${encodeURIComponent(cursor)}`),
  configs: () => request<{ items: Config[] }>('/api/v1/configs'),
  config: (name: string) => request<Config>(`/api/v1/configs/${encodeURIComponent(name)}`),
  revisions: (name: string) => request<{ items: ConfigRevision[] }>(`/api/v1/configs/${encodeURIComponent(name)}/revisions`),
  validateConfig: (name: string, contentYaml: string) => request<{ valid: boolean; contentHash: string }>(`/api/v1/configs/${encodeURIComponent(name)}/validate`, { method: 'POST', body: JSON.stringify({ contentYaml }) }),
  saveDraft: (name: string, contentYaml: string, baseRevision: string) => request<{ draftId: string }>(`/api/v1/configs/${encodeURIComponent(name)}/drafts`, { method: 'POST', body: JSON.stringify({ contentYaml, baseRevision }) }),
  publishConfig: (name: string, contentYaml: string, note: string) => request<{ revision: string }>(`/api/v1/configs/${encodeURIComponent(name)}/publish`, { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey() }, body: JSON.stringify({ contentYaml, note }) }),
  rollbackConfig: (name: string, revision: string, note: string) => request<{ revision: string }>(`/api/v1/configs/${encodeURIComponent(name)}/revisions/${encodeURIComponent(revision)}/rollback`, { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey() }, body: JSON.stringify({ note }) }),
  playerCommand: (id: string, action: string, payload: unknown) => request<Record<string, unknown>>(`/api/v1/players/${encodeURIComponent(id)}/${action}`, { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey() }, body: JSON.stringify({ payload }) }),
  broadcastAudience: () => request<BroadcastAudience>('/api/v1/broadcast/audience'),
  // 幂等键由调用方传入：全服公告影响所有在线玩家，键必须在打开确认框时固定下来，
  // 重试复用同一个，双击或网络重连才不会变成第二条公告。
  announce: (lines: string[], centerBroadcast: boolean, idempotencyKey: string) => request<Record<string, unknown>>('/api/v1/broadcast/announce', { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify({ payload: { lines, centerBroadcast } }) }),
  // 全服邮件比公告更重：它会给每个角色（含离线）写一封带附件的邮件，发错了只能
  // 一个个角色去删。幂等键同理必须固定，否则双击或断网重试就是第二封。
  mailAll: (title: string, content: string, items: { itemId: number; count: number }[], idempotencyKey: string) => request<Record<string, unknown>>('/api/v1/broadcast/mail', { method: 'POST', headers: { 'Idempotency-Key': idempotencyKey }, body: JSON.stringify({ payload: { title, content, items } }) }),
  subGMs: () => request<{ items: SubGM[] }>('/api/v1/sub-gms'),
  createSubGM: (username: string, password: string, displayName: string) => request<SubGM>('/api/v1/sub-gms', { method: 'POST', body: JSON.stringify({ username, password, displayName }) }),
  subGMBindingsFor: (id: string) => request<{ items: SubGMBinding[]; limit: number }>(`/api/v1/sub-gms/${encodeURIComponent(id)}/bindings`),
  unbindSubGM: (id: string, accountId: string) => request<{ unbound: boolean }>(`/api/v1/sub-gms/${encodeURIComponent(id)}/bindings/${encodeURIComponent(accountId)}`, { method: 'DELETE', body: '{}' }),
  subGMBindings: () => request<{ items: SubGMBinding[]; limit: number }>('/api/v1/sub-gm/bindings'),
  bindSubGMAccount: (account: string, password: string) => request<{ accountId: string; bound: boolean }>('/api/v1/sub-gm/bindings', { method: 'POST', body: JSON.stringify({ account, password }) }),
}
