import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, BookOpen, Coins, Diamond, Gem, Gift, KeyRound, Mail, PawPrint, RefreshCw, Repeat2, ScrollText, Send, ShieldAlert, SlidersHorizontal, Sparkles, Sword, Users, Wand2, X } from 'lucide-react'
import { FormEvent, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { can, useAuth } from '../../layouts/Shell'
import { api, CatalogSkill, PlayerDetail as Player } from '../../api/client'
import { Button, Card, EmptyState, ErrorState, Field, LoadingState, PageHeader, StatusPill } from '../../components/Ui'
import { ItemPicker, SelectedItem, itemSelectionError, itemSelectionPayload } from '../../components/ItemPicker'
import { EquipManualDraft, EquipManualPanel, emptyEquipManualDraft, equipKeyOf, equipManualError, equipManualPayload, equipTarget, integerField, manualGemById, manualQuality, manualRandomPool, resizeRowIds } from './EquipManualPanel'
import { EquipAffixDraft, EquipAffixPanel, emptyEquipAffixDraft, equipAffixError, equipAffixPayload } from './EquipAffixPanel'
import { EquipGemDraft, EquipGemSlot, EquipGemPanel, emptyEquipGemDraft, equipGemError, equipGemPayload, gemHoleLabel, gemLevelOf } from './EquipGemPanel'
import { formatCoin, formatCoinCopper } from '../../lib/money'

type Action = 'kick' | 'reload' | 'currency-adjust' | 'level-adjust' | 'pet-adjust' | 'skills-adjust' | 'starsoul-adjust' | 'equip-adjust' | 'equip-manual' | 'equip-gem' | 'equip-affix' | 'job-adjust' | 'activity-adjust' | 'stats-adjust' | 'family-reset' | 'mail' | 'items/grant'
type CommandResult = Record<string, unknown>
type Feedback = { tone: 'success' | 'info'; text: string }

const exactFloatIntegerLimit = 16_777_216n
const resourceLabels: Record<string, string> = {
  detail: '概览', inventory: '背包', equipment: '装备', skills: '技能', tasks: '任务', pets: '宠物', starsouls: '星魂', activity: '活动', mails: '邮件',
}
const actionMeta: Record<Action, { title: string; icon: typeof Send; permission: string }> = {
  kick: { title: '强制下线', icon: ShieldAlert, permission: 'players.kick' },
  reload: { title: '刷新角色', icon: RefreshCw, permission: 'players.reload' },
  'currency-adjust': { title: '调整货币', icon: Coins, permission: 'players.currency' },
  'level-adjust': { title: '调整等级 / 经验', icon: Sparkles, permission: 'players.level' },
  'pet-adjust': { title: '调整宠物', icon: PawPrint, permission: 'players.pet' },
  'skills-adjust': { title: '调整技能', icon: BookOpen, permission: 'players.skills' },
  'starsoul-adjust': { title: '调整星魂', icon: Gem, permission: 'players.starsoul' },
  'equip-adjust': { title: '调整装备', icon: Sword, permission: 'players.equip' },
  // 手工装备只管星级 / 品质 / 随机属性。强化、宝石、词缀各有独立入口，
  // 标题里不再罗列那三样，免得运营以为还得在这里一起提交。
  'equip-manual': { title: '手工装备（星级/品质/随机属性）', icon: Wand2, permission: 'players.equip' },
  // 宝石是独立入口：不需要走星级/随机属性/词缀那一整套校验，给普通装备换宝石用这个。
  'equip-gem': { title: '镶嵌宝石', icon: Diamond, permission: 'players.equip' },
  // 词缀同样独立：词缀池和槽位数由品质决定，一次提交整组词缀。
  'equip-affix': { title: '调整词缀', icon: ScrollText, permission: 'players.equip' },
  'job-adjust': { title: '转职 / 转生', icon: Repeat2, permission: 'players.job' },
  'activity-adjust': { title: '调整日常次数', icon: KeyRound, permission: 'players.activity' },
  'stats-adjust': { title: '调整活力 / 加点', icon: SlidersHorizontal, permission: 'players.stats' },
  // 家族归属的修复入口：家族被绕过正常流程删掉后，角色会被自己的脏数据锁死
  // （建家族报「已有家族」、开面板报「家族不存在」），游戏里没有任何操作能恢复。
  'family-reset': { title: '重置家族归属', icon: Users, permission: 'players.family' },
  mail: { title: '发送邮件', icon: Mail, permission: 'players.mail' },
  'items/grant': { title: '发放物品', icon: Gift, permission: 'players.items' },
}
const currencyLabels: Record<string, string> = {
  coin: '金币', yuanBao: '元宝', voucher: '代金券', starCoin: '星币', honor: '荣誉',
  pvpCurrency: '竞技币', familyContribution: '家族贡献',
}
const fieldLabels: Record<string, string> = {
  server_id: '服务器 ID', sort_order: '排序', get_source: '获取来源', is_locked: '是否锁定', item_count: '物品数量',
  item_id: '物品名称', item_type: '物品类型', location: '位置', quality: '品质', level: '等级', skill_id: '技能名称',
  slot_index: '槽位', mail_id: '邮件 ID', title: '标题', content: '内容', sender_name: '发件人', state: '状态',
  remain_time: '剩余时间', task_id: '任务 ID', kill_count: '击杀数量', star: '星级', strength_level: '强化等级',
  special_key: '特殊键', special_id: '特殊 ID', last_day: '最后日期', last_month: '最后月份', month_count: '本月次数',
  pvp_score: 'PVP 积分', pvp_battle_day: 'PVP 战斗日期', pvp_battle_count: 'PVP 战斗次数', pvp_match_count: 'PVP 匹配次数',
  pet_id: '宠物 ID', name: '名称', exp: '经验', intimacy: '亲密度', is_show: '跟随显示', active: '活跃度',
  star_soul_id: '星魂 ID', type_id: '星魂类型', pos_type: '部位', main_attribute: '主属性', vice_growth_level: '副属性成长',
  space_travel_remaining: '时空旅行次数', death_tower_remaining: '死亡之塔次数', family_boss_keys: '家族 Boss 钥匙', dungeon_quota_day: '次数刷新日',
  eat_count: '今日喂食', pet_state: '宠物状态', action_end: '动作结束时间', rewarded: '已领奖励',
}
const attributeLabels: Record<string, string> = { str: '力量', quk: '敏捷', spi: '精神', wim: '智慧', phy: '体质', sta: '耐力' }
const jobOptions = [
  { id: '1', name: '军官' }, { id: '2', name: '运动员' }, { id: '3', name: '护士' }, { id: '4', name: '超人' },
]


function skillOptionsForRow(jobSkills: CatalogSkill[], rows: { skillId: string }[], index: number, currentId: string, catalog: CatalogSkill[]): CatalogSkill[] {
  const used = new Set(rows.map((row, rowIndex) => rowIndex === index ? '' : row.skillId).filter(Boolean))
  const options = jobSkills.filter(item => !used.has(String(item.id)))
  if (currentId && !options.some(item => String(item.id) === currentId)) {
    const current = catalog.find(item => String(item.id) === currentId)
    options.unshift(current || { id: Number(currentId), name: '未收录技能', jobType: 0, jobName: '' })
  }
  return options
}

function jobTypeOf(jobId: number) {
  if (!jobId || jobId <= 0) return 1
  if (jobId <= 8) return Math.floor((jobId + 1) / 2)
  if (jobId < 10000) return Math.floor(jobId / 1000)
  if (jobId < 100000) return Math.floor(jobId / 10000)
  return Math.floor(jobId / 100000)
}

function locationLabel(location: unknown) {
  if (String(location) === '2') return '身上'
  if (String(location) === '3') return '仓库'
  if (String(location) === '1') return '背包'
  return formatValue(location)
}

function labelField(field: string) {
  return fieldLabels[field] || field.replaceAll('_', ' ').replace(/(^| )([a-z])/g, (_, prefix: string, letter: string) => `${prefix}${letter.toUpperCase()}`)
}

function formatValue(value: unknown) {
  if (typeof value === 'number') return new Intl.NumberFormat('zh-CN').format(value)
  if (typeof value === 'string' && /^-?\d+$/.test(value)) {
    try { return BigInt(value).toLocaleString('zh-CN') } catch { return value }
  }
  return String(value ?? '—')
}

function formatTimestamp(value: unknown) {
  const numeric = typeof value === 'number' || (typeof value === 'string' && /^\d{9,}$/.test(value))
  if (!numeric) return String(value ?? '—')
  const date = new Date(Number(value) * 1000)
  return Number.isNaN(date.valueOf()) ? String(value) : date.toLocaleString('zh-CN', { hour12: false })
}

function objectValue(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function exceedsExactFloatInteger(value: unknown) {
  if (typeof value !== 'string' || !/^-?\d+$/.test(value)) return false
  try {
    const integer = BigInt(value)
    return integer > exactFloatIntegerLimit || integer < -exactFloatIntegerLimit
  } catch {
    return false
  }
}

function commandFeedback(action: Action, result: CommandResult): Feedback {
  if (result.status === 'pending') return { tone: 'info', text: String(result.message || '命令已发送，正在等待游戏服确认。') }
  const data = objectValue(result.data)
  if (action === 'kick') {
    return data.online === true
      ? { tone: 'success', text: '已发送强制下线通知，角色会话正在清理。' }
      : { tone: 'info', text: String(data.message || '角色当前不在线，无需下线。') }
  }
  if (action === 'reload') {
    return data.online === true
      ? { tone: 'success', text: '角色数据与背包快照已重新同步。' }
      : { tone: 'info', text: '角色当前不在线；持久化数据未发生变化。' }
  }
  if (action === 'currency-adjust') {
    const currency = String(data.currency || '')
    const label = currencyLabels[currency] || currency || '货币'
    const detail = `${label}已调整：${formatValue(data.before)} → ${formatValue(data.after)}（变更 ${formatValue(data.delta)}）`
    const precision = exceedsExactFloatInteger(data.after)
      ? '。数据库保存的是精确整数；游戏客户端使用 float32 显示，大额余额末位可能舍入'
      : ''
    return { tone: 'success', text: `${detail}${precision}。` }
  }
  if (action === 'level-adjust') {
    const extras = []
    if (data.beforeCharPoint !== undefined && data.afterCharPoint !== undefined && data.beforeCharPoint !== data.afterCharPoint) {
      extras.push(`潜能点 ${formatValue(data.beforeCharPoint)} → ${formatValue(data.afterCharPoint)}`)
    }
    if (data.beforeSkillPoint !== undefined && data.afterSkillPoint !== undefined && data.beforeSkillPoint !== data.afterSkillPoint) {
      extras.push(`技能点 ${formatValue(data.beforeSkillPoint)} → ${formatValue(data.afterSkillPoint)}`)
    }
    return { tone: 'success', text: extras.length ? `等级与经验已更新，${extras.join('，')}。` : '等级与经验已更新并同步给在线角色。' }
  }
  if (action === 'pet-adjust') {
    const after = objectValue(data.after)
    return { tone: 'success', text: `宠物已更新：${after.name || ''} ID ${formatValue(after.petId)} Lv.${formatValue(after.level)}。` }
  }
  if (action === 'skills-adjust') return { tone: 'success', text: '技能与技能点已更新并同步给在线角色。' }
  if (action === 'starsoul-adjust') return { tone: 'success', text: '星魂已更新并同步给在线角色。' }
  if (action === 'equip-adjust') return { tone: 'success', text: '装备已更新并同步给在线角色。' }
  if (action === 'equip-manual') {
    const after = objectValue(data.after)
    const count = (value: unknown) => (Array.isArray(value) ? value.length : 0)
    return {
      tone: 'success',
      text: `手工装备已更新：品质 ${formatValue(after.quality)} · 星级 ${formatValue(after.star)}，随机属性 ${count(after.randomAttrs)} 条，已同步给在线角色。强化 / 洗练词缀 / 宝石未改动。`,
    }
  }
  if (action === 'equip-gem') {
    const after = objectValue(data.after)
    return { tone: 'success', text: `宝石已更新：${gemHoleLabel(after.gems)}已镶嵌，装备的星级 / 品质 / 随机属性 / 词缀 / 强化均未改动，已同步给在线角色。` }
  }
  if (action === 'equip-affix') {
    const after = objectValue(data.after)
    const rows = Array.isArray(after.addAttrs) ? after.addAttrs.length : 0
    return { tone: 'success', text: `洗练词缀已更新：当前 ${rows} 条，装备的星级 / 品质 / 随机属性 / 强化 / 宝石均未改动，已同步给在线角色。` }
  }
  if (action === 'job-adjust') return { tone: 'success', text: '职业 / 转生已更新并同步给在线角色。' }
  if (action === 'activity-adjust') return { tone: 'success', text: '日常次数已更新。' }
  if (action === 'stats-adjust') return { tone: 'success', text: '活力 / 潜能点 / 加点已更新并同步给在线角色。' }
  if (action === 'family-reset') {
    const familyId = formatValue(data.familyId)
    if (!familyId || familyId === '0') return { tone: 'info', text: '该角色当前没有家族，无需重置。' }
    const name = typeof data.familyName === 'string' && data.familyName ? `「${data.familyName}」` : ''
    const parts: string[] = []
    if (data.dangling === true) parts.push('库里已经没有这个家族，只清掉了残留归属和 BOSS 数据')
    else if (data.dissolved === true) parts.push('家族已解散')
    else parts.push('家族保留')
    if (formatValue(data.promotedLeader) !== '0' && formatValue(data.promotedLeader) !== '') parts.push(`新族长 #${formatValue(data.promotedLeader)}`)
    const removed = Array.isArray(data.removedMembers) ? data.removedMembers.length : 0
    return { tone: 'success', text: `已重置家族 ${familyId}${name}：${parts.join('，')}，清除 ${removed} 名成员的归属，在线角色已同步。` }
  }
  if (action === 'mail') return { tone: 'success', text: Number(data.items) > 0 ? `邮件已发送，包含 ${data.items} 项附件，角色领取后到账。` : '邮件已发送。' }
  if (action === 'items/grant') return { tone: 'success', text: '物品已发放，在线角色背包已刷新。' }
  return { tone: 'success', text: '操作已完成。' }
}

function ResourceTable({ items, itemNames, itemNamesLoading }: {
  items: Record<string, unknown>[]
  itemNames: ReadonlyMap<string, string>
  itemNamesLoading: boolean
}) {
  if (!items.length) return <EmptyState />
  const columns = Object.keys(items[0]).filter(column => !(column === 'skill_id' && 'name' in items[0])).slice(0, 12)
  const renderCell = (column: string, value: unknown) => {
    if (column === 'item_id') {
      if (itemNamesLoading) return <span className="muted">读取名称…</span>
      const name = itemNames.get(String(value))
      return name ? <span>{name}</span> : <span className="muted">未收录物品</span>
    }
    if (column === 'location') return locationLabel(value)
    return column.endsWith('_time') || column.endsWith('_at') ? formatTimestamp(value) : formatValue(value)
  }
  return <div className="table-wrap"><table className="data-table"><thead><tr>{columns.map(column => <th key={column}>{column === 'name' && 'skill_id' in items[0] ? '技能名称' : labelField(column)}</th>)}</tr></thead><tbody>{items.map((item, index) => <tr key={index}>{columns.map(column => <td className={column === 'item_id' ? undefined : 'mono'} key={column}>{renderCell(column, item[column])}</td>)}</tr>)}</tbody></table></div>
}

function ActionModal({ player, action, close, done }: { player: Player; action: Action; close: () => void; done: (result: CommandResult) => void }) {
  const meta = actionMeta[action]
  const Icon = meta.icon
  const [amount, setAmount] = useState('')
  const [currency, setCurrency] = useState('coin')
  const [level, setLevel] = useState(String(player.level))
  const [exp, setExp] = useState(player.exp)
  const [petId, setPetId] = useState('2101')
  const [petLevel, setPetLevel] = useState('1')
  const [petExp, setPetExp] = useState('0')
  const [petIntimacy, setPetIntimacy] = useState('0')
  const [petName, setPetName] = useState('宠物01')
  const [petShow, setPetShow] = useState(true)
  const [petActive, setPetActive] = useState('0')
  const [petEatCount, setPetEatCount] = useState('0')
  const [petResetAction, setPetResetAction] = useState(true)
  const [skillPoint, setSkillPoint] = useState(String(player.skillPoint ?? 0))
  const [skillRows, setSkillRows] = useState<{ skillId: string; level: string }[]>([{ skillId: '', level: '1' }])
  const [starSoulMode, setStarSoulMode] = useState<'grant' | 'adjust'>('grant')
  const [starSoulId, setStarSoulId] = useState('')
  const [starTypeId, setStarTypeId] = useState('1001')
  const [starPosType, setStarPosType] = useState('0')
  const [starQuality, setStarQuality] = useState('6')
  const [starLevel, setStarLevel] = useState('0')
  const [starExp, setStarExp] = useState('0')
  const [starCount, setStarCount] = useState('1')
  const [starLocked, setStarLocked] = useState(false)
  const [starRemove, setStarRemove] = useState(false)
  const [starEquip, setStarEquip] = useState('')
  const [equipKey, setEquipKey] = useState('') // `${location}|${slotIndex}`，与 player_items 主键一致
  const [equipStrength, setEquipStrength] = useState('1')
  const [equipStar, setEquipStar] = useState('1')
  const [equipLocked, setEquipLocked] = useState(false)
  const [equipClearGems, setEquipClearGems] = useState(false)
  const [manualDraft, setManualDraft] = useState<EquipManualDraft>(emptyEquipManualDraft)
  const manualBackfilled = useRef('')
  const [gemDraft, setGemDraft] = useState<EquipGemDraft>(emptyEquipGemDraft)
  const gemBackfilled = useRef('')
  const [affixDraft, setAffixDraft] = useState<EquipAffixDraft>(emptyEquipAffixDraft)
  const affixBackfilled = useRef('')
  const [jobType, setJobType] = useState(String(jobTypeOf(player.jobId)))
  const [transLevel, setTransLevel] = useState(String(player.trans ?? 0))
  const [spaceTravel, setSpaceTravel] = useState('0')
  const [deathTower, setDeathTower] = useState('0')
  const [familyBossKeys, setFamilyBossKeys] = useState('0')
  const [energy, setEnergy] = useState(String(player.energy ?? 0))
  const [charPoint, setCharPoint] = useState(String(player.charPoint ?? 0))
  const [strAdd, setStrAdd] = useState(String(player.attributes?.str ?? 0))
  const [qukAdd, setQukAdd] = useState(String(player.attributes?.quk ?? 0))
  const [spiAdd, setSpiAdd] = useState(String(player.attributes?.spi ?? 0))
  const [wimAdd, setWimAdd] = useState(String(player.attributes?.wim ?? 0))
  const [phyAdd, setPhyAdd] = useState(String(player.attributes?.phy ?? 0))
  const [staAdd, setStaAdd] = useState(String(player.attributes?.sta ?? 0))
  // 默认「摘除角色」：只在角色被错误地关在某个家族里时才动他，不动别人。
  const [familyMode, setFamilyMode] = useState('detach')
  const [title, setTitle] = useState('运营补发')
  const [content, setContent] = useState('')
  const [items, setItems] = useState<SelectedItem[]>([])
  const [error, setError] = useState('')
  const [resultFeedback, setResultFeedback] = useState<Feedback | null>(null)
  const modalRef = useRef<HTMLFormElement>(null)
  const petOptions = [
    { id: '2101', name: '宠物01' }, { id: '2102', name: '宠物02' }, { id: '2103', name: '宠物03' },
    { id: '2104', name: '宠物04' }, { id: '2105', name: '宠物05' },
  ]
  const petQuery = useQuery({
    queryKey: ['player-resource', player.id, 'pets'],
    queryFn: () => api.playerResource(player.id, 'pets'),
    enabled: action === 'pet-adjust',
  })
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    modalRef.current?.focus()
    return () => previous?.focus()
  }, [])
  const skillQuery = useQuery({
    queryKey: ['player-resource', player.id, 'skills'],
    queryFn: () => api.playerResource(player.id, 'skills'),
    enabled: action === 'skills-adjust',
  })
  const skillCatalogQuery = useQuery({
    queryKey: ['skill-catalog'],
    queryFn: api.skills,
    staleTime: 30_000,
    enabled: action === 'skills-adjust',
  })
  const skillCatalog = skillCatalogQuery.data?.items || []
  const jobSkillOptions = skillCatalog.filter(item => item.jobType === jobTypeOf(player.jobId))
  useEffect(() => {
    const current = petQuery.data?.items?.[0]
    if (!current) return
    setPetId(String(current.pet_id ?? '2101'))
    setPetLevel(String(current.level ?? '1'))
    setPetExp(String(current.exp ?? '0'))
    setPetIntimacy(String(current.intimacy ?? '0'))
    setPetName(String(current.name ?? '宠物01'))
    setPetShow(current.is_show === true || current.is_show === '1' || current.is_show === 1)
    setPetActive(String(current.active ?? '0'))
    setPetEatCount(String(current.eat_count ?? '0'))
  }, [petQuery.data])
  useEffect(() => {
    setSkillPoint(String(player.skillPoint ?? 0))
    const rows = (skillQuery.data?.items || []).map(item => ({ skillId: String(item.skill_id ?? ''), level: String(item.level ?? '1') })).filter(item => item.skillId)
    setSkillRows(rows.length ? rows : [{ skillId: '', level: '1' }])
  }, [player.skillPoint, skillQuery.data])
  const starSoulQuery = useQuery({
    queryKey: ['player-resource', player.id, 'starsouls'],
    queryFn: () => api.playerResource(player.id, 'starsouls'),
    enabled: action === 'starsoul-adjust',
  })
  const starSoulOptions = [
    { id: '1001', name: '太阳烛照' }, { id: '1002', name: '太阴幽荧' }, { id: '1003', name: '青龙' }, { id: '1004', name: '白虎' },
    { id: '1005', name: '朱雀' }, { id: '1006', name: '玄武' }, { id: '1007', name: '应龙' }, { id: '1008', name: '黄龙' },
    { id: '1009', name: '螣蛇' }, { id: '1010', name: '勾陈' },
  ]
  useEffect(() => {
    const current = (starSoulQuery.data?.items || []).find(item => String(item.star_soul_id ?? '') === starSoulId) || starSoulQuery.data?.items?.[0]
    if (!current) return
    setStarSoulId(String(current.star_soul_id ?? ''))
    setStarTypeId(String(current.type_id ?? '1001'))
    setStarPosType(String(current.pos_type ?? '0'))
    setStarQuality(String(current.quality ?? '6'))
    setStarLevel(String(current.level ?? '0'))
    setStarExp(String(current.exp ?? '0'))
    setStarLocked(current.is_locked === true || current.is_locked === '1' || current.is_locked === 1)
    setStarEquip(current.slot_index === undefined || current.slot_index === null || current.slot_index === '' ? '' : '1')
  }, [starSoulQuery.data])
  // 三个装备类面板（调整装备 / 手工装备 / 镶嵌宝石）都要拉装备列表和物品目录，
  // 另外两个还要拉候选池和子表状态。集中成两个开关，避免每加一个面板就漏改一处 enabled。
  const needsEquipList = action === 'equip-adjust' || action === 'equip-manual' || action === 'equip-gem' || action === 'equip-affix'
  const equipOps = action === 'equip-manual' || action === 'equip-gem' || action === 'equip-affix'
  const inventoryQuery = useQuery({
    queryKey: ['player-resource', player.id, 'inventory'],
    queryFn: () => api.playerResource(player.id, 'inventory'),
    enabled: needsEquipList,
  })
  const activityQuery = useQuery({
    queryKey: ['player-resource', player.id, 'activity'],
    queryFn: () => api.playerResource(player.id, 'activity'),
    enabled: action === 'activity-adjust',
  })
  const itemCatalogQuery = useQuery({
    queryKey: ['item-catalog'],
    queryFn: api.items,
    staleTime: 30_000,
    enabled: needsEquipList,
  })
  const equipNames = useMemo(() => new Map((itemCatalogQuery.data?.items || []).map(item => [String(item.id), item.name])), [itemCatalogQuery.data])
  const equipItems = (inventoryQuery.data?.items || []).filter(item => String(item.item_type) === '1' || String(item.location) === '2')
  // 当前选中的那一行（「调整装备」面板用）。查不到就说明键是旧的或装备已经不在列表里，
  // 此时必须停下来报错，不能让 payload 带着空 location 出去（服务端只会回「位置不能为空」）。
  const equipSelectedRow = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === equipKey) || null
  // 手工装备 / 调整词缀：静态候选池（随机属性档位 / 词缀）加上两/三张子表里的当前状态。
  // 首次打开先落到第一件装备上，与「调整装备」面板一致。
  // 定位用 location + slot_index（player_items 的主键），**不用 server_id**：
  // 新生成的装备 server_id 恒为 0，拿 0 定位会撞上所有新装备。
  const manualSelectedKey = manualDraft.equipKey || equipKeyOf(equipItems[0]?.location, equipItems[0]?.slot_index)
  const manualEquipItem = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === manualSelectedKey)
  const manualItemId = manualEquipItem ? String(manualEquipItem.item_id ?? '') : ''
  // 调整词缀：与手工装备同一套候选池（服务端也是同一个 /equip-manual/options 接口），
  // 所以共用一个查询，只是选中的装备各自独立。
  const affixSelectedKey = affixDraft.equipKey || equipKeyOf(equipItems[0]?.location, equipItems[0]?.slot_index)
  const affixEquipItem = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === affixSelectedKey)
  // 词缀池和槽位数都跟装备**当前**品质走，所以面板要用的是库里那一行的品质，不是草稿里的。
  const affixQuality = Number(affixEquipItem?.quality ?? 1)
  // 镶嵌宝石：与手工装备同一套候选池和同一张部位规则表（服务端也是同一个
  // /equip-manual/options 接口），所以共用一个查询，只是选中的装备各自独立。
  const gemSelectedKey = gemDraft.equipKey || equipKeyOf(equipItems[0]?.location, equipItems[0]?.slot_index)
  const gemEquipItem = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === gemSelectedKey)
  const gemItemId = gemEquipItem ? String(gemEquipItem.item_id ?? '') : ''
  const gemLocked = gemEquipItem
    ? gemEquipItem.is_locked === true || gemEquipItem.is_locked === '1' || gemEquipItem.is_locked === 1
    : false
  const manualOptionsQuery = useQuery({
    queryKey: ['equip-manual-options'],
    queryFn: () => api.equipManualOptions(),
    staleTime: 5 * 60_000,
    enabled: equipOps,
  })
  // 只有宝石面板需要「这件装备的部位规则」（EquipBase.Type / MaxHole / 允许的宝石），
  // 手工和词缀都只用静态候选池，所以这个查询只在宝石面板打开时才发。
  const ruleItemId = action === 'equip-gem' ? gemItemId : ''
  const equipRuleQuery = useQuery({
    queryKey: ['equip-manual-options', ruleItemId],
    queryFn: () => api.equipManualOptions(ruleItemId),
    staleTime: 5 * 60_000,
    enabled: equipOps && !!ruleItemId,
  })
  const manualRandomQuery = useQuery({
    queryKey: ['player-resource', player.id, 'random-attributes'],
    queryFn: () => api.playerResource(player.id, 'random-attributes'),
    enabled: equipOps,
  })
  const manualAffixQuery = useQuery({
    queryKey: ['player-resource', player.id, 'affixes'],
    queryFn: () => api.playerResource(player.id, 'affixes'),
    enabled: equipOps,
  })
  const manualGemQuery = useQuery({
    queryKey: ['player-resource', player.id, 'gems'],
    queryFn: () => api.playerResource(player.id, 'gems'),
    enabled: equipOps,
  })
  const manualOptions = manualOptionsQuery.data || null
  const gemRule = gemItemId ? equipRuleQuery.data?.equipRules?.[gemItemId] || null : null
  // 回填只在「选中的装备或它的持久化状态变了」时跑一次：运营正在编辑时不要被后台刷新打断。
  // 随机属性表只在手工面板用得上、词缀表只在词缀面板用得上，所以 key 里各带各的，
  // 一个面板的持久化状态变化不会把另一个面板正在编辑的草稿冲掉。
  // 手工面板还要等候选池（manualOptionsQuery）：回填时要按品质的候选池补默认随机属性，
  // 池子晚到就得重跑一次。
  const manualStateKey = [manualSelectedKey, manualItemId, manualOptionsQuery.dataUpdatedAt,
    manualRandomQuery.dataUpdatedAt, inventoryQuery.dataUpdatedAt].join('|')
  const affixStateKey = [affixSelectedKey, affixQuality,
    manualOptionsQuery.dataUpdatedAt, manualAffixQuery.dataUpdatedAt, inventoryQuery.dataUpdatedAt].join('|')
  const manualEquipOptions = equipItems.map(item => {
    const serverId = String(item.server_id ?? '')
    const locked = item.is_locked === true || item.is_locked === '1' || item.is_locked === 1
    const name = equipNames.get(String(item.item_id)) || String(item.item_id)
    return {
      key: equipKeyOf(item.location, item.slot_index),
      serverId, location: String(item.location ?? ''), slotIndex: String(item.slot_index ?? ''), locked,
      label: `${locationLabel(item.location)} · ${name}${serverId && serverId !== '0' ? ` #${serverId}` : ''} 槽位${String(item.slot_index ?? '')} 品质${String(item.quality ?? 1)} 星${String(item.star ?? 0)} 强${String(item.strength_level ?? item.level ?? 0)}${locked ? '（已锁定）' : ''}`,
    }
  })
  const manualEquipOption = manualEquipOptions.find(item => item.key === manualSelectedKey) || null
  useEffect(() => {
    if (action !== 'equip-manual' || !manualSelectedKey) return
    if (manualBackfilled.current === `${manualSelectedKey}|${manualStateKey}`) return
    const current = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === manualSelectedKey)
    const location = String(current?.location ?? '')
    const slotIndex = String(current?.slot_index ?? '')
    const picked = (manualRandomQuery.data?.items || [])
      .filter(row => String(row.location ?? '') === location && String(row.slot_index ?? '') === slotIndex)
      .sort((left, right) => Number(left.position ?? 0) - Number(right.position ?? 0))
      .map(row => String(row.attribute_id ?? ''))
    const star = String(current?.star ?? 1)
    const quality = String(current?.quality ?? '1')
    // 随机属性表的主键是 (player_id, location, slot_index, position)，position 可能稀疏，
    // 这里不按 position 铺定长数组，而是按**星级**铺：星级 N 就必须正好 N 行。
    // 玩法侧新发放的装备天生是「EquipBase.Star 星级 + 0 条随机属性」（repairBagItem
    // 只补 Star，不补 RandomAttrs），照搬库里那条会渲染成 0 行下拉，运营无从下手，
    // 一提交就被拒。所以不足的行按该品质的候选池补上默认值，多出来的截掉。
    // 候选池没到就先补空行（resizeRowIds 的 pool 为空时补空串），池子到了
    // manualOptionsQuery.dataUpdatedAt 会改 key，这个 effect 再跑一次把默认值填上。
    const pool = manualOptions ? manualRandomPool(manualOptions, manualQuality(manualOptions, quality)).map(attr => String(attr.id)) : []
    const randomRows = resizeRowIds(picked, integerField(star) ?? 0, pool)
    manualBackfilled.current = `${manualSelectedKey}|${manualStateKey}`
    setManualDraft({
      equipKey: manualSelectedKey,
      star,
      quality: String(current?.quality ?? '1'),
      randomRows,
    })
  }, [action, manualSelectedKey, manualStateKey])
  // 镶嵌宝石的回填：只取这件装备的宝石槽，其余字段一概不碰（服务端 action 也只改 GemList）。
  // 候选池的 dataUpdatedAt 必须进 key：每孔的等级是靠宝石 ID 反查 GemLevel 得到的，
  // 池子没到就只能回填成空等级，等它到了要重新回填一次才对得上。
  const gemStateKey = [gemSelectedKey, gemItemId, equipRuleQuery.dataUpdatedAt,
    manualOptionsQuery.dataUpdatedAt, manualGemQuery.dataUpdatedAt, inventoryQuery.dataUpdatedAt].join('|')
  useEffect(() => {
    if (action !== 'equip-gem' || !gemSelectedKey) return
    if (gemBackfilled.current === `${gemSelectedKey}|${gemStateKey}`) return
    const current = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === gemSelectedKey)
    const location = String(current?.location ?? '')
    const slotIndex = String(current?.slot_index ?? '')
    const gemRows = (manualGemQuery.data?.items || [])
      .filter(row => String(row.location ?? '') === location && String(row.slot_index ?? '') === slotIndex)
      .sort((left, right) => Number(left.position ?? 0) - Number(right.position ?? 0))
    // 子表主键是 (player_id, location, slot_index, position)，position 可能稀疏，
    // 所以先按 position 铺成定长数组，长度取 MaxHole 和库里最大孔位 + 1 的较大值。
    const holeCount = Math.max(gemRule?.maxHole ?? 0, gemRows.length ? Number(gemRows[gemRows.length - 1].position ?? 0) + 1 : 0)
    // 每孔回填成 { level, gem }：等级取自库里那颗宝石自己的 GemLevel，这样下拉默认停在
    // 它所属的等级上。空槽（0 / 缺行）的 level 留空，由面板统一归到该部位的最低等级。
    // player_item_gems 是定长存储，空槽也会有一行 gem_item_id = 0，所以 0 必须当成空槽。
    const slots: EquipGemSlot[] = Array.from({ length: holeCount }, (_, index) => {
      const raw = Number(gemRows.find(row => Number(row.position ?? 0) === index)?.gem_item_id ?? 0)
      if (!(raw > 0)) return { level: '', gem: '' }
      const id = String(raw)
      const gem = manualOptions ? manualGemById(manualOptions, id) : undefined
      return { level: gem ? String(gemLevelOf(gem)) : '', gem: id }
    })
    gemBackfilled.current = `${gemSelectedKey}|${gemStateKey}`
    setGemDraft({ equipKey: gemSelectedKey, slots })
  }, [action, gemSelectedKey, gemStateKey])
  const gemEquipOptions = manualEquipOptions
  const gemEquipOption = gemEquipOptions.find(item => item.key === gemSelectedKey) || null
  // 调整词缀的回填：只取这件装备的词缀，其余字段一概不碰（服务端 action 也只改 AddAttrs）。
  // affix_id 是 (player_id, location, slot_index, position) 主键里的定长行，按 position 排序后
  // 直接就是词缀顺序，不需要像随机属性那样按星级补齐——服务端要的是「整组提交」，
  // 条数不对会被它按词缀池补齐/裁掉，面板负责把这个数字说清楚。
  useEffect(() => {
    if (action !== 'equip-affix' || !affixSelectedKey) return
    if (affixBackfilled.current === `${affixSelectedKey}|${affixStateKey}`) return
    const current = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === affixSelectedKey)
    const location = String(current?.location ?? '')
    const slotIndex = String(current?.slot_index ?? '')
    const affixRows = (manualAffixQuery.data?.items || [])
      .filter(row => String(row.location ?? '') === location && String(row.slot_index ?? '') === slotIndex)
      .sort((left, right) => Number(left.position ?? 0) - Number(right.position ?? 0))
      .map(row => String(row.affix_id ?? ''))
    affixBackfilled.current = `${affixSelectedKey}|${affixStateKey}`
    setAffixDraft({ equipKey: affixSelectedKey, affixRows })
  }, [action, affixSelectedKey, affixStateKey])
  const affixEquipOption = manualEquipOptions.find(item => item.key === affixSelectedKey) || null
  useEffect(() => {
    const current = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === equipKey) || equipItems[0]
    if (!current) return
    setEquipKey(equipKeyOf(current.location, current.slot_index))
    setEquipStrength(String(current.strength_level ?? current.level ?? '1'))
    setEquipStar(String(current.star ?? '1'))
    setEquipLocked(current.is_locked === true || current.is_locked === '1' || current.is_locked === 1)
  }, [inventoryQuery.data])
  useEffect(() => {
    const current = activityQuery.data?.items?.[0]
    if (!current) return
    setSpaceTravel(String(current.space_travel_remaining ?? '0'))
    setDeathTower(String(current.death_tower_remaining ?? '0'))
    setFamilyBossKeys(String(current.family_boss_keys ?? '0'))
  }, [activityQuery.data])
  useEffect(() => {
    setJobType(String(jobTypeOf(player.jobId)))
    setTransLevel(String(player.trans ?? 0))
    setEnergy(String(player.energy ?? 0))
    setCharPoint(String(player.charPoint ?? 0))
    setStrAdd(String(player.attributes?.str ?? 0))
    setQukAdd(String(player.attributes?.quk ?? 0))
    setSpiAdd(String(player.attributes?.spi ?? 0))
    setWimAdd(String(player.attributes?.wim ?? 0))
    setPhyAdd(String(player.attributes?.phy ?? 0))
    setStaAdd(String(player.attributes?.sta ?? 0))
  }, [player])
  const mutation = useMutation({
    mutationFn: () => {
      let payload: unknown = {}
      if (action === 'currency-adjust') payload = { currency, delta: amount }
      if (action === 'level-adjust') payload = { level, exp }
      if (action === 'pet-adjust') payload = { petId, level: petLevel, exp: petExp, intimacy: petIntimacy, name: petName, isShow: petShow, active: petActive, eatCount: petEatCount, resetAction: petResetAction }
      if (action === 'skills-adjust') payload = { skillPoint, skills: skillRows.filter(row => row.skillId.trim()).map(row => ({ skillId: row.skillId.trim(), level: row.level.trim() })) }
      if (action === 'starsoul-adjust') {
        payload = starSoulMode === 'grant'
          ? { typeId: starTypeId, posType: starPosType, quality: starQuality, level: starLevel, exp: starExp, count: starCount, isLocked: starLocked, ...(starEquip === '' ? {} : { equip: starEquip === '1' }) }
          : { starSoulId, level: starRemove ? undefined : starLevel, exp: starRemove ? undefined : starExp, isLocked: starRemove ? undefined : starLocked, remove: starRemove, ...(starRemove || starEquip === '' ? {} : { equip: starEquip === '1' }) }
      }
      if (action === 'equip-adjust') {
        payload = { ...equipTarget(equipSelectedRow?.location, equipSelectedRow?.slot_index, equipSelectedRow?.server_id), strength: equipStrength, star: equipStar, isLocked: equipLocked, clearGems: equipClearGems }
      }
      if (action === 'equip-manual') payload = equipManualPayload(manualDraft, manualEquipOption)
      if (action === 'equip-gem') payload = equipGemPayload(gemDraft, gemRule, gemLocked, gemEquipOption)
      if (action === 'equip-affix') payload = equipAffixPayload(affixDraft, affixEquipOption)
      if (action === 'job-adjust') payload = jobType === String(jobTypeOf(player.jobId)) ? { trans: transLevel } : { jobType, trans: transLevel }
      if (action === 'activity-adjust') payload = { spaceTravelRemaining: spaceTravel, deathTowerRemaining: deathTower, familyBossKeys }
      if (action === 'stats-adjust') payload = { energy, charPoint, str: strAdd, quk: qukAdd, spi: spiAdd, wim: wimAdd, phy: phyAdd, sta: staAdd }
      if (action === 'family-reset') payload = { mode: familyMode }
      if (action === 'mail') payload = { title, content, items: itemSelectionPayload(items) }
      if (action === 'items/grant') payload = { items: itemSelectionPayload(items) }
      return api.playerCommand(player.id, action, payload)
    },
    onSuccess: value => { setResultFeedback(commandFeedback(action, value)); done(value) },
    onError: err => setError(err instanceof Error ? err.message : '操作失败'),
  })
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (mutation.isPending) return
    setResultFeedback(null)
    if (action === 'currency-adjust' && !amount) { setError('请输入调整数量'); return }
    if (action === 'pet-adjust') {
      if (!petId.trim() || !petLevel.trim() || !petName.trim()) { setError('请填写宠物 ID、等级和名称'); return }
    }
    if (action === 'skills-adjust') {
      if (!skillPoint.trim()) { setError('请填写技能点'); return }
      const rows = skillRows.filter(row => row.skillId.trim())
      if (rows.some(row => !row.level.trim())) { setError('请填写技能等级，0 表示遗忘'); return }
    }
    if (action === 'starsoul-adjust') {
      if (starSoulMode === 'grant' && (!starTypeId.trim() || !starPosType.trim() || !starQuality.trim() || !starCount.trim())) { setError('请填写星魂类型、部位、品质和数量'); return }
      if (starSoulMode === 'adjust' && !starSoulId.trim()) { setError('请选择要调整的星魂'); return }
    }
    if (action === 'equip-adjust') {
      // 该角色一件可调整的装备都没有时，说清楚原因，别让运营对着空下拉猜。
      if (inventoryQuery.isLoading) { setError('装备列表还在读取，请稍后重试'); return }
      if (!equipItems.length) { setError('该角色没有可调整的装备，请先发放或穿戴装备'); return }
      if (!equipSelectedRow) { setError('选中的装备已不在列表里，请重新选择'); return }
      if (!equipStrength.trim() || !equipStar.trim()) { setError('请填写强化和星级'); return }
    }
    if (action === 'equip-manual') {
      if (!manualOptions || inventoryQuery.isLoading) { setError('候选池或装备列表还在读取，请稍后重试'); return }
      if (!equipItems.length) { setError('该角色没有可调整的装备，请先发放或穿戴装备'); return }
      const invalid = equipManualError(manualDraft, manualOptions)
      if (invalid) { setError(invalid); return }
    }
    if (action === 'equip-affix') {
      if (!manualOptions || inventoryQuery.isLoading) { setError('候选池或装备列表还在读取，请稍后重试'); return }
      if (!equipItems.length) { setError('该角色没有可调整词缀的装备，请先发放或穿戴装备'); return }
      const invalid = equipAffixError(affixDraft, manualOptions, affixQuality)
      if (invalid) { setError(invalid); return }
    }
    if (action === 'equip-gem') {
      if (!manualOptions || inventoryQuery.isLoading) { setError('候选池或装备列表还在读取，请稍后重试'); return }
      if (!equipItems.length) { setError('该角色没有可镶嵌宝石的装备，请先发放或穿戴装备'); return }
      const invalid = equipGemError(gemDraft, manualOptions, gemRule, gemLocked)
      if (invalid) { setError(invalid); return }
    }
    if (action === 'job-adjust') {
      if (!['1', '2', '3', '4'].includes(jobType)) { setError('请选择职业'); return }
      if (!transLevel.trim()) { setError('请填写转生次数'); return }
    }
    if (action === 'activity-adjust') {
      if (!spaceTravel.trim() || !deathTower.trim() || !familyBossKeys.trim()) { setError('请填写时空旅行、死亡之塔和家族 Boss 钥匙'); return }
    }
    if (action === 'stats-adjust') {
      if (!energy.trim() || !charPoint.trim() || !strAdd.trim() || !qukAdd.trim() || !spiAdd.trim() || !wimAdd.trim() || !phyAdd.trim() || !staAdd.trim()) { setError('请填写活力、潜能点和六维加点'); return }
    }
    if (action === 'items/grant' || action === 'mail') {
      const invalid = itemSelectionError(items, action === 'items/grant')
      if (invalid) { setError(invalid); return }
    }
    if (action === 'mail') {
      const bytes = (value: string) => new TextEncoder().encode(value).length
      if (!title.trim()) { setError('请填写邮件标题'); return }
      if (bytes(title) > 191) { setError('邮件标题过长，请缩短后重试'); return }
      if (bytes(content) > 4000) { setError('邮件内容过长，请精简后重试'); return }
    }
    setError('')
    mutation.mutate()
  }
  return <div className="modal-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget && !mutation.isPending) close() }}>
    <form ref={modalRef} tabIndex={-1} role="dialog" aria-modal="true" aria-labelledby="player-action-title" className={`modal${action === 'items/grant' || action === 'mail' ? ' modal-with-items' : ''}`} onSubmit={submit} onKeyDown={event => {
      if (event.key === 'Escape' && !mutation.isPending) { event.preventDefault(); close() }
      if (event.key === 'Tab') {
        const focusable = [...event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled), a[href]')].filter(element => element.getClientRects().length > 0)
        const first = focusable[0], last = focusable[focusable.length - 1]
        if (event.shiftKey && (document.activeElement === first || document.activeElement === event.currentTarget)) { event.preventDefault(); last?.focus() }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
      }
    }}>
      <div className="modal-header"><h2 id="player-action-title"><Icon size={17} style={{ verticalAlign: 'middle', marginRight: 7 }} />{meta.title}</h2><button type="button" className="icon-btn" onClick={close} disabled={mutation.isPending} aria-label="关闭"><X size={18} /></button></div>
      <p>目标角色：<strong>{player.name}</strong> <span className="muted mono">#{player.id}</span></p>
      {action === 'kick' ? <div className="notice danger">在线角色将收到原生下线通知，旧会话进入受控清理流程。</div> : null}
      {action === 'reload' ? <div className="notice">重新从持久化状态载入角色数据，适用于修复显示异常。</div> : null}
      {action === 'currency-adjust' ? <>
        <div className="field-row"><Field label="货币类型"><select className="select" value={currency} onChange={event => setCurrency(event.target.value)}><option value="coin">金币</option><option value="yuanBao">元宝</option><option value="voucher">代金券</option><option value="starCoin">星币</option><option value="honor">荣誉</option><option value="pvpCurrency">竞技币</option><option value="familyContribution">家族贡献</option></select></Field><Field label="调整数量" hint={currency === 'coin' ? '按金币填写，1 金 = 10000 铜；正数增加、负数扣除' : '正数增加，负数扣除'}><input className="input" inputMode="numeric" value={amount} onChange={event => setAmount(event.target.value)} required /></Field></div>
        <div className="notice" style={{ marginTop: 13 }}>游戏货币协议使用 float32；余额超过 16,777,216 后，客户端末位可能舍入，数据库仍保存精确整数。</div>
      </> : null}
      {action === 'level-adjust' ? <>
        <div className="field-row"><Field label="目标等级"><input className="input" inputMode="numeric" value={level} onChange={event => setLevel(event.target.value)} required /></Field><Field label="经验值"><input className="input" inputMode="numeric" value={exp} onChange={event => setExp(event.target.value)} required /></Field></div>
        <div className="notice" style={{ marginTop: 13 }}>升级会按正式规则补潜能点（每级 1 点）和技能点（每 200 级 1 点）。降级不扣回已发点数。</div>
      </> : null}
      {action === 'pet-adjust' ? <>
        {petQuery.isLoading ? <LoadingState /> : null}
        {petQuery.isError ? <div className="notice danger">当前宠物读取失败，仍可按下面的值创建或覆盖。</div> : null}
        {!petQuery.isLoading && !(petQuery.data?.items || []).length ? <div className="notice">该角色还没有宠物记录，确认后会创建。</div> : null}
        <div className="field-row"><Field label="宠物"><select className="select" value={petId} onChange={event => { const next = event.target.value; setPetId(next); const option = petOptions.find(item => item.id === next); if (option) setPetName(option.name) }}>{petOptions.some(item => item.id === petId) ? null : <option value={petId}>{petId}</option>}{petOptions.map(item => <option key={item.id} value={item.id}>{item.name}（{item.id}）</option>)}</select></Field><Field label="名称"><input className="input" value={petName} onChange={event => setPetName(event.target.value)} required /></Field></div>
        <div className="field-row"><Field label="等级"><input className="input" inputMode="numeric" value={petLevel} onChange={event => setPetLevel(event.target.value)} required /></Field><Field label="经验"><input className="input" inputMode="numeric" value={petExp} onChange={event => setPetExp(event.target.value)} required /></Field></div>
        <div className="field-row"><Field label="亲密度"><input className="input" inputMode="numeric" value={petIntimacy} onChange={event => setPetIntimacy(event.target.value)} required /></Field><Field label="活跃度"><input className="input" inputMode="numeric" value={petActive} onChange={event => setPetActive(event.target.value)} required /></Field></div>
        <div className="field-row"><Field label="今日喂食次数" hint="0 到 10"><input className="input" inputMode="numeric" value={petEatCount} onChange={event => setPetEatCount(event.target.value)} required /></Field><Field label="跟随显示"><select className="select" value={petShow ? '1' : '0'} onChange={event => setPetShow(event.target.value === '1')}><option value="1">显示</option><option value="0">隐藏</option></select></Field></div>
        <Field label="结束当前动作"><select className="select" value={petResetAction ? '1' : '0'} onChange={event => setPetResetAction(event.target.value === '1')}><option value="1">结束嬉戏 / 锻炼 / 探险</option><option value="0">保持当前动作</option></select></Field>
      </> : null}
      {action === 'skills-adjust' ? <>
        {skillQuery.isLoading || skillCatalogQuery.isLoading ? <LoadingState /> : null}
        <Field label="技能点" hint="直接设为目标值，不消耗技能书"><input className="input" inputMode="numeric" value={skillPoint} onChange={event => setSkillPoint(event.target.value)} required /></Field>
        {skillRows.map((row, index) => {
          const options = skillOptionsForRow(jobSkillOptions, skillRows, index, row.skillId, skillCatalog)
          return <div className="field-row" key={index}><Field label={index === 0 ? '技能' : ''}><select className="select" value={row.skillId} onChange={event => setSkillRows(list => list.map((item, itemIndex) => itemIndex === index ? { ...item, skillId: event.target.value } : item))}><option value="">选择技能</option>{options.map(item => <option key={item.id} value={String(item.id)}>{item.name}</option>)}</select></Field><Field label={index === 0 ? '等级' : ''} hint={index === 0 ? '0 表示遗忘，普攻不能忘' : undefined}><input className="input" inputMode="numeric" value={row.level} onChange={event => setSkillRows(list => list.map((item, itemIndex) => itemIndex === index ? { ...item, level: event.target.value } : item))} /></Field></div>
        })}
        <Button type="button" variant="secondary" onClick={() => setSkillRows(list => [...list, { skillId: '', level: '1' }])}>增加技能</Button>
      </> : null}
      {action === 'starsoul-adjust' ? <>
        {starSoulQuery.isLoading ? <LoadingState /> : null}
        <Field label="操作"><select className="select" value={starSoulMode} onChange={event => { setStarSoulMode(event.target.value as 'grant' | 'adjust'); setStarRemove(false) }}><option value="grant">发放新星魂</option><option value="adjust">调整已有星魂</option></select></Field>
        {starSoulMode === 'adjust' ? <Field label="星魂"><select className="select" value={starSoulId} onChange={event => {
          const next = event.target.value; setStarSoulId(next)
          const current = (starSoulQuery.data?.items || []).find(item => String(item.star_soul_id ?? '') === next)
          if (!current) return
          setStarTypeId(String(current.type_id ?? '1001')); setStarPosType(String(current.pos_type ?? '0')); setStarQuality(String(current.quality ?? '6'))
          setStarLevel(String(current.level ?? '0')); setStarExp(String(current.exp ?? '0'))
          setStarLocked(current.is_locked === true || current.is_locked === '1' || current.is_locked === 1)
          setStarEquip(current.slot_index === undefined || current.slot_index === null || current.slot_index === '' ? '' : '1')
        }}>{(starSoulQuery.data?.items || []).map(item => <option key={String(item.star_soul_id)} value={String(item.star_soul_id)}>{starSoulOptions.find(option => option.id === String(item.type_id))?.name || String(item.type_id)} #{String(item.star_soul_id)} Lv.{String(item.level)}</option>)}{(starSoulQuery.data?.items || []).length ? null : <option value="">暂无星魂</option>}</select></Field> : null}
        {starSoulMode === 'grant' ? <div className="field-row"><Field label="类型"><select className="select" value={starTypeId} onChange={event => setStarTypeId(event.target.value)}>{starSoulOptions.map(item => <option key={item.id} value={item.id}>{item.name}（{item.id}）</option>)}</select></Field><Field label="部位 0-11"><input className="input" inputMode="numeric" value={starPosType} onChange={event => setStarPosType(event.target.value)} required /></Field></div> : null}
        {starSoulMode === 'grant' ? <div className="field-row"><Field label="品质 1-6" hint="6 为天工"><input className="input" inputMode="numeric" value={starQuality} onChange={event => setStarQuality(event.target.value)} required /></Field><Field label="数量" hint="1 到 20"><input className="input" inputMode="numeric" value={starCount} onChange={event => setStarCount(event.target.value)} required /></Field></div> : null}
        {starSoulMode === 'adjust' && starRemove ? null : <div className="field-row"><Field label="等级" hint="0 到 20，不消耗材料"><input className="input" inputMode="numeric" value={starLevel} onChange={event => setStarLevel(event.target.value)} /></Field><Field label="经验"><input className="input" inputMode="numeric" value={starExp} onChange={event => setStarExp(event.target.value)} /></Field></div>}
        {starSoulMode === 'adjust' && starRemove ? null : <div className="field-row"><Field label="锁定"><select className="select" value={starLocked ? '1' : '0'} onChange={event => setStarLocked(event.target.value === '1')}><option value="1">锁定</option><option value="0">未锁定</option></select></Field><Field label="穿戴"><select className="select" value={starEquip} onChange={event => setStarEquip(event.target.value)}><option value="">保持现状</option><option value="1">穿戴</option><option value="0">卸下</option></select></Field></div>}
        {starSoulMode === 'adjust' ? <Field label="删除这枚星魂"><select className="select" value={starRemove ? '1' : '0'} onChange={event => setStarRemove(event.target.value === '1')}><option value="0">否</option><option value="1">删除</option></select></Field> : null}
      </> : null}
      {action === 'equip-adjust' ? <>
        {inventoryQuery.isLoading ? <LoadingState /> : null}
        {inventoryQuery.isError ? <div className="notice danger">装备列表读取失败，可稍后重试。</div> : null}
        {!inventoryQuery.isLoading && !equipItems.length ? <div className="notice">该角色没有可调整的装备。</div> : null}
        <Field label="装备" hint="含背包、身上和仓库，按「位置 + 槽位」定位"><select className="select" value={equipKey} onChange={event => {
          const next = event.target.value; setEquipKey(next); setEquipClearGems(false)
          const current = equipItems.find(item => equipKeyOf(item.location, item.slot_index) === next)
          if (!current) return
          setEquipStrength(String(current.strength_level ?? current.level ?? '1')); setEquipStar(String(current.star ?? '1'))
          setEquipLocked(current.is_locked === true || current.is_locked === '1' || current.is_locked === 1)
        }}>{equipItems.map(item => <option key={equipKeyOf(item.location, item.slot_index)} value={equipKeyOf(item.location, item.slot_index)}>{locationLabel(item.location)} · {equipNames.get(String(item.item_id)) || String(item.item_id)} 槽位{String(item.slot_index ?? '')} 强{String(item.strength_level ?? item.level ?? 1)} 星{String(item.star ?? 0)}</option>)}{equipItems.length ? null : <option value="">暂无装备</option>}</select></Field>
        <div className="field-row"><Field label="强化 0-20"><input className="input" inputMode="numeric" value={equipStrength} onChange={event => setEquipStrength(event.target.value)} required /></Field><Field label="星级 0-20" hint="填 0 会被服务端按该装备的基础星级补回"><input className="input" inputMode="numeric" value={equipStar} onChange={event => setEquipStar(event.target.value)} required /></Field></div>
        <div className="field-row"><Field label="锁定"><select className="select" value={equipLocked ? '1' : '0'} onChange={event => setEquipLocked(event.target.value === '1')}><option value="1">锁定</option><option value="0">未锁定</option></select></Field><Field label="卸下宝石" hint="卸到背包"><select className="select" value={equipClearGems ? '1' : '0'} onChange={event => setEquipClearGems(event.target.value === '1')}><option value="0">不卸</option><option value="1">卸下并回背包</option></select></Field></div>
      </> : null}
      {action === 'equip-manual' ? <>
        {manualOptionsQuery.isLoading || inventoryQuery.isLoading ? <LoadingState /> : null}
        {manualOptionsQuery.isError ? <div className="notice danger" role="alert">候选池读取失败，请稍后重试。</div> : null}
        {inventoryQuery.isError ? <div className="notice danger" role="alert">装备列表读取失败，可稍后重试。</div> : null}
        {!inventoryQuery.isLoading && !equipItems.length ? <div className="notice">该角色没有可调整的装备。</div> : null}
        {manualOptions ? <>
          <EquipManualPanel draft={manualDraft} onChange={setManualDraft} options={manualOptions} equipOptions={manualEquipOptions} disabled={mutation.isPending} />
          <div className="notice" style={{ marginTop: 13 }}>回填的星级 / 品质 / 随机属性读的是数据库最近保存的状态，在线角色尚未保存的改动可能滞后。强化请用「调整装备」，洗练词缀请用「调整词缀」，宝石请用「镶嵌宝石」。</div>
        </> : null}
      </> : null}
      {action === 'equip-gem' ? <>
        {manualOptionsQuery.isLoading || inventoryQuery.isLoading ? <LoadingState /> : null}
        {manualOptionsQuery.isError ? <div className="notice danger" role="alert">宝石候选池读取失败，请稍后重试。</div> : null}
        {inventoryQuery.isError ? <div className="notice danger" role="alert">装备列表读取失败，可稍后重试。</div> : null}
        {!inventoryQuery.isLoading && !equipItems.length ? <div className="notice">该角色没有可镶嵌宝石的装备。</div> : null}
        {manualOptions ? <>
          {equipRuleQuery.isError ? <div className="notice danger" role="alert">这件装备的部位规则读取失败，宝石孔暂时不能编辑。</div> : null}
          <EquipGemPanel draft={gemDraft} onChange={setGemDraft} options={manualOptions} rule={gemRule} locked={gemLocked} equipOptions={gemEquipOptions} disabled={mutation.isPending} />
          <div className="notice" style={{ marginTop: 13 }}>回填的宝石读的是数据库最近保存的状态，在线角色尚未保存的改动可能滞后。这个操作只改宝石，星级 / 品质 / 随机属性 / 洗练词缀 / 强化都不会被动到。</div>
        </> : null}
      </> : null}
      {action === 'equip-affix' ? <>
        {manualOptionsQuery.isLoading || inventoryQuery.isLoading ? <LoadingState /> : null}
        {manualOptionsQuery.isError ? <div className="notice danger" role="alert">词缀候选池读取失败，请稍后重试。</div> : null}
        {inventoryQuery.isError ? <div className="notice danger" role="alert">装备列表读取失败，可稍后重试。</div> : null}
        {!inventoryQuery.isLoading && !equipItems.length ? <div className="notice">该角色没有可调整词缀的装备。</div> : null}
        {manualOptions ? <>
          <EquipAffixPanel draft={affixDraft} onChange={setAffixDraft} options={manualOptions} quality={affixQuality} equipOptions={manualEquipOptions} disabled={mutation.isPending} />
          <div className="notice" style={{ marginTop: 13 }}>回填的词缀读的是数据库最近保存的状态，在线角色尚未保存的改动可能滞后。这个操作只改洗练词缀，星级 / 品质 / 随机属性 / 强化 / 宝石都不会被动到。</div>
        </> : null}
      </> : null}
      {action === 'job-adjust' ? <>
        <div className="field-row"><Field label="职业"><select className="select" value={jobType} onChange={event => setJobType(event.target.value)}>{jobOptions.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field><Field label="转生 0-3"><input className="input" inputMode="numeric" value={transLevel} onChange={event => setTransLevel(event.target.value)} required /></Field></div>
        <div className="notice" style={{ marginTop: 13 }}>转职会卸下职业不符的已穿装备，并清掉非本职业技能（返还技能点）。只改转生时保持当前职业即可。</div>
      </> : null}
      {action === 'activity-adjust' ? <>
        {activityQuery.isLoading ? <LoadingState /> : null}
        {activityQuery.isError ? <div className="notice danger">日常次数读取失败，可按目标值直接覆盖。</div> : null}
        <div className="field-row"><Field label="时空旅行剩余"><input className="input" inputMode="numeric" value={spaceTravel} onChange={event => setSpaceTravel(event.target.value)} required /></Field><Field label="死亡之塔剩余"><input className="input" inputMode="numeric" value={deathTower} onChange={event => setDeathTower(event.target.value)} required /></Field></div>
        <Field label="家族 Boss 钥匙"><input className="input" inputMode="numeric" value={familyBossKeys} onChange={event => setFamilyBossKeys(event.target.value)} required /></Field>
      </> : null}
      {action === 'stats-adjust' ? <>
        <div className="notice">仅正式 GM 可改活力 / 潜能点 / 六维加点，子 GM 无此权限。</div>
        <div className="field-row"><Field label="活力"><input className="input" inputMode="numeric" value={energy} onChange={event => setEnergy(event.target.value)} required /></Field><Field label="潜能点"><input className="input" inputMode="numeric" value={charPoint} onChange={event => setCharPoint(event.target.value)} required /></Field></div>
        <div className="field-row"><Field label="力量"><input className="input" inputMode="numeric" value={strAdd} onChange={event => setStrAdd(event.target.value)} required /></Field><Field label="敏捷"><input className="input" inputMode="numeric" value={qukAdd} onChange={event => setQukAdd(event.target.value)} required /></Field></div>
        <div className="field-row"><Field label="精神"><input className="input" inputMode="numeric" value={spiAdd} onChange={event => setSpiAdd(event.target.value)} required /></Field><Field label="智慧"><input className="input" inputMode="numeric" value={wimAdd} onChange={event => setWimAdd(event.target.value)} required /></Field></div>
        <div className="field-row"><Field label="体质"><input className="input" inputMode="numeric" value={phyAdd} onChange={event => setPhyAdd(event.target.value)} required /></Field><Field label="耐力"><input className="input" inputMode="numeric" value={staAdd} onChange={event => setStaAdd(event.target.value)} required /></Field></div>
      </> : null}
      {action === 'family-reset' ? <>
        {player.familyId && player.familyId !== '0'
          ? <div className="notice">该角色当前记录的家族：<strong>#{player.familyId}</strong>。家族贡献 {formatValue(player.familyContribute)}，个人贡献 {formatValue(player.personalContribute)}。</div>
          : <div className="notice">该角色当前没有家族记录。家族被直接删过之后，在线会话里可能还留着旧 ID，执行一次即可清掉。</div>}
        <Field label="处理方式" hint="摘除只动这一个角色；解散会清掉整个家族全体成员">
          <select className="select" value={familyMode} onChange={event => setFamilyMode(event.target.value)}>
            <option value="detach">仅把该角色摘出家族</option>
            <option value="dissolve">解散该角色所在的整个家族</option>
          </select>
        </Field>
        <div className={familyMode === 'dissolve' ? 'notice danger' : 'notice'} style={{ marginTop: 13 }}>
          {familyMode === 'dissolve'
            ? '整个家族的成员归属、申请表、家族 BOSS 进度与领取记录都会被清除，家族名随即释放。此操作不可撤销。'
            : '角色是族长且家族还有别人时，族长自动让给剩余成员；摘完只剩他一个，家族随之解散。'}
        </div>
      </> : null}
      {action === 'mail' ? <><Field label="邮件标题"><input className="input" value={title} onChange={event => setTitle(event.target.value)} disabled={mutation.isPending} required /></Field><Field label="邮件内容"><textarea className="textarea" style={{ minHeight: 100 }} value={content} onChange={event => setContent(event.target.value)} disabled={mutation.isPending} required /></Field></> : null}
      {action === 'items/grant' || action === 'mail' ? <ItemPicker items={items} onChange={setItems} disabled={mutation.isPending} optional={action === 'mail'} /> : null}
      {error ? <div className="notice danger" role="alert" style={{ marginTop: 13 }}>{error}</div> : null}
      <div className="modal-actions" style={{ flexWrap: 'wrap' }}>
        {resultFeedback ? <div className={`notice ${resultFeedback.tone}`} role="status" aria-live="polite" style={{ flexBasis: '100%' }}>{resultFeedback.text}</div> : null}
        <Button type="button" variant="secondary" disabled={mutation.isPending} onClick={close}>{resultFeedback ? '关闭' : '取消'}</Button><Button type="submit" loading={mutation.isPending}>确认执行</Button>
      </div>
    </form>
  </div>
}

export function PlayerDetail() {
  const session = useAuth()
  const { playerId = '' } = useParams()
  const [tab, setTab] = useState('detail')
  const [action, setAction] = useState<Action | null>(null)
  const [feedback, setFeedback] = useState<Feedback | null>(null)
  const queryClient = useQueryClient()
  const player = useQuery({ queryKey: ['player', playerId], queryFn: () => api.player(playerId), enabled: !!playerId })
  const resource = useQuery({ queryKey: ['player-resource', playerId, tab], queryFn: () => api.playerResource(playerId, tab), enabled: !!playerId && tab !== 'detail' })
  const itemCatalog = useQuery({ queryKey: ['item-catalog'], queryFn: api.items, staleTime: 30_000, enabled: tab === 'inventory' || tab === 'equipment' })
  const itemNames = useMemo(() => new Map((itemCatalog.data?.items || []).map(item => [String(item.id), item.name])), [itemCatalog.data])
  if (player.isLoading) return <><PageHeader title="角色详情" /><LoadingState /></>
  if (player.isError || !player.data) return <><PageHeader title="角色详情" /><ErrorState message={player.error instanceof Error ? player.error.message : '角色不存在'} /></>
  const p = player.data
  const finishAction = (result: CommandResult) => {
    if (action) setFeedback(commandFeedback(action, result))
    void queryClient.invalidateQueries({ queryKey: ['player', playerId] })
    void queryClient.invalidateQueries({ queryKey: ['player-resource', playerId] })
  }
  return <>
    <PageHeader eyebrow="OPERATIONS / PLAYER" title={p.name || '未命名角色'} description={`角色 #${p.id} · 账号 ${p.account}`} action={<Link className="btn btn-secondary" to="/players"><ArrowLeft size={15} />返回角色列表</Link>} />
    <div className="action-bar">{(Object.keys(actionMeta) as Action[]).filter(key => can(session, actionMeta[key].permission)).map(key => { const meta = actionMeta[key]; const Icon = meta.icon; return <Button key={key} variant={key === 'kick' ? 'danger' : 'secondary'} onClick={() => { setFeedback(null); setAction(key) }}><Icon size={14} />{meta.title}</Button> })}</div>
    {feedback ? <div className={`notice ${feedback.tone}`} role="status" aria-live="polite" style={{ marginBottom: 16 }}>{feedback.text}</div> : null}
    <div className="detail-grid">
      <Card className="identity-card"><div className="identity-head"><div className="identity-avatar">{(p.name || '角').slice(0, 1)}</div><div><h2>{p.name || '未命名'}</h2><small className="mono">{p.online ? '在线' : '离线'} · Lv.{p.level}</small></div></div><div className="kv-list"><div className="kv"><span>账号</span><span>{p.account}</span></div><div className="kv"><span>职业 / 转生</span><span>{jobOptions.find(item => item.id === String(jobTypeOf(p.jobId)))?.name || p.jobId} / T{p.trans}</span></div><div className="kv"><span>位置</span><span>地图 {p.mapId}<br />{p.posX.toFixed(1)}, {p.posY.toFixed(1)}</span></div><div className="kv"><span>金币</span><span title={`${formatCoinCopper(p.coin)} 铜`}>{formatCoin(p.coin)}</span></div><div className="kv"><span>元宝</span><span>{formatValue(p.yuanBao)}</span></div><div className="kv"><span>代金券</span><span>{formatValue(p.voucher)}</span></div><div className="kv"><span>荣誉</span><span>{formatValue(p.honor)}</span></div><div className="kv"><span>竞技币</span><span>{formatValue(p.pvpCurrency)}</span></div><div className="kv"><span>家族</span><span>{p.familyId && p.familyId !== '0' ? `#${p.familyId}` : '无'}</span></div><div className="kv"><span>家族贡献</span><span>{formatValue(p.familyContribute)}</span></div><div className="kv"><span>最后登录</span><span>{formatTimestamp(p.lastLogin)}</span></div></div><div style={{ marginTop: 19 }}><StatusPill status={p.online ? 'online' : 'offline'}>{p.online ? '在线 · Redis 标记' : '离线'}</StatusPill></div></Card>
      <Card><div className="tabs" role="tablist">{Object.entries(resourceLabels).map(([key, label]) => <button type="button" role="tab" aria-selected={tab === key} key={key} className={tab === key ? 'tab active' : 'tab'} onClick={() => setTab(key)}>{label}</button>)}</div>{tab === 'detail' ? <div className="stat-block"><div className="kv"><span>经验</span><span>{formatValue(p.exp)}</span></div><div className="kv"><span>活力</span><span>{formatValue(p.energy)}</span></div><div className="kv"><span>潜能点</span><span>{formatValue(p.charPoint)}</span></div><div className="kv"><span>技能点</span><span>{formatValue(p.skillPoint)}</span></div>{Object.entries(p.attributes).map(([key, value]) => <div className="kv" key={key}><span>属性 · {attributeLabels[key] || key}</span><span>{formatValue(value)}</span></div>)}</div> : resource.isLoading ? <LoadingState /> : resource.isError ? <ErrorState message={resource.error instanceof Error ? resource.error.message : undefined} onRetry={() => resource.refetch()} /> : <ResourceTable items={resource.data?.items || []} itemNames={itemNames} itemNamesLoading={itemCatalog.isLoading} />}</Card>
    </div>
    {action ? <ActionModal player={p} action={action} close={() => setAction(null)} done={finishAction} /> : null}
  </>
}
