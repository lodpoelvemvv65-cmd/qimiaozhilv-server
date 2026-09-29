import { Field } from '../../components/Ui'
import { EquipManualAffix, EquipManualOptions } from '../../api/client'

// 手工装备面板（GM action player.equip_manual）。
//
// 规则与 server-mysql/gm_control_equip_manual.go 一一对应，这里只做**即时反馈**，
// 服务端仍然是唯一权威：它能给出的错，这里也要能看到同一个数字。
//
// 面板范围刻意收窄成「装备 + 星级 + 品质 + 随机属性」四件事。强化、宝石、洗练词缀
// 都各自有独立入口，不在这里重复：
//   - 强化 / 星级 / 锁定 / 卸宝石 → 「调整装备」（player.equip_adjust）；
//   - 宝石槽 → 「镶嵌宝石」（player.equip_gem）；
//   - 洗练词缀 → 「调整词缀」（player.equip_affix）。
// 一个面板塞四套规则，任何一处不满足都会让整单被拒，运营改个星级还得先看懂
// 宝石部位规则；拆开之后每个入口的报错都只关于自己那件事。
//
// 三条容易写错的约束（照抄服务端注释）：
//  1. 星级 N ⇔ 随机属性正好 N 条（RandomAttrs 不受登录修复影响，纯 GM 侧约束）；
//  2. 星级下限是 1：equip.go repairBagItem 对 Star==0 的装备会补回 EquipBase.Star，
//     0 存不住，服务端直接拒绝；
//  3. 品质决定随机属性的档位（ManulEquipAttribute 的 id/100 == 品质），改品质会把
//     已挑的随机属性全部作废，所以品质变化时整组重填。
//
// 定位方式：用 location + slotIndex，**不用 server_id**。player_items 的主键是
// (player_id, location, slot_index)，而 server_id 对新生成的装备恒为 0
// （schema 默认 0，只有 cmd/grant-items 会发号），拿 0 去定位会同时撞上所有新装备，
// 服务端也会直接报「装备实例不能小于 1」。serverId 只在非 0 时一并带上。

export type EquipManualDraft = {
  equipKey: string // `${location}|${slotIndex}`
  star: string
  quality: string
  randomRows: string[]
}

export type EquipManualEquipOption = { key: string; label: string; location: string; slotIndex: string; serverId: string; locked: boolean }

export const emptyEquipManualDraft: EquipManualDraft = {
  equipKey: '', star: '1', quality: '1', randomRows: [],
}

// equipKeyOf 是装备在面板里的唯一标识，与 player_items 的主键一致。
// 缺任一字段时返回空串而不是 '|'：调用方一律用 `!key` 判断「没选中装备」，
// 返回 '|' 会被当成有效键，最后把空 location 发给服务端，报「位置不能为空」。
export function equipKeyOf(location: unknown, slotIndex: unknown): string {
  if (location === undefined || location === null || slotIndex === undefined || slotIndex === null) return ''
  return `${String(location)}|${String(slotIndex)}`
}

// equipTarget 把一行装备还原成服务端 payload 的定位字段（gmFindEquip 的输入）。
// location + slotIndex 是可靠的那条路；serverId 必须是 ≥1 的实例号，而新生成装备的
// server_id 恒为 0，传 0 会被判「装备实例不能小于 1」，所以只在非 0 时才带上。
export function equipTarget(location: unknown, slotIndex: unknown, serverId: unknown) {
  const id = String(serverId ?? '')
  return {
    ...(id && id !== '0' ? { serverId: id } : {}),
    location: String(location ?? ''),
    slotIndex: String(slotIndex ?? ''),
  }
}

export function integerField(text: string): number | null {
  const trimmed = text.trim()
  if (!/^\d+$/.test(trimmed)) return null
  const parsed = Number.parseInt(trimmed, 10)
  return Number.isSafeInteger(parsed) ? parsed : null
}

// resizeRowIds 把多行 id 列表调整到 count 行：多则截断，少则从 pool 里挑还没用过的补上。
// pool 为空时补空串，交给下拉框让运营自己选。
export function resizeRowIds(rows: string[], count: number, pool: string[]): string[] {
  const next = rows.slice(0, Math.max(count, 0))
  while (next.length < count) {
    const used = new Set(next.filter(Boolean))
    next.push(pool.find(id => !used.has(id)) ?? '')
  }
  return next
}

export function manualQuality(options: EquipManualOptions, quality: string): number {
  const parsed = integerField(quality)
  if (parsed === null) return options.limits.minQuality
  return Math.min(Math.max(parsed, options.limits.minQuality), options.limits.maxQuality)
}

// manualBonusCount 对应服务端 equipmentBonusCount：白绿 1、蓝紫 2、橙红 3，
// 并且像服务端一样收敛到词缀池大小（池子比槽位少时以池子为准）。
export function manualBonusCount(options: EquipManualOptions, quality: number): number {
  const target = options.limits.bonusCountByQuality[String(quality)] ?? 1
  const pool = options.affixPools.byQuality[String(quality)] || []
  return pool.length > 0 && target > pool.length ? pool.length : target
}

export function manualRandomPool(options: EquipManualOptions, quality: number) {
  return options.randomAttrsByQuality[String(quality)] || []
}

// 词缀候选：普通池（品质决定档位）+ 六维池。六维在玩法侧被整条排除出 affixPoolForQuality，
// 只能单独产出，所以两条来源都要列出来，但六维会标出来「只能 1 条」。
export function manualAffixOptions(options: EquipManualOptions, quality: number): EquipManualAffix[] {
  const ids = new Set([...(options.affixPools.byQuality[String(quality)] || []), ...options.affixPools.sixDimension])
  return options.affixes.filter(affix => ids.has(affix.id))
}

export function manualAffixById(options: EquipManualOptions, id: string): EquipManualAffix | undefined {
  return options.affixes.find(affix => String(affix.id) === id)
}

export function manualGemById(options: EquipManualOptions, id: string) {
  return options.gems.find(gem => String(gem.id) === id)
}

export function manualRandomById(options: EquipManualOptions, id: string) {
  for (const list of Object.values(options.randomAttrsByQuality)) {
    const found = list.find(attr => String(attr.id) === id)
    if (found) return found
  }
  return undefined
}

// equipManualError 镜像服务端校验，只为让运营在点「确认执行」之前就看到同一个数字。
// 服务端 gm_control_equip_manual.go 的文案才是最终口径（报错会写出具体上限/下限）。
export function equipManualError(draft: EquipManualDraft, options: EquipManualOptions): string {
  if (!draft.equipKey) return '请选择要调整的装备'
  const star = integerField(draft.star)
  if (star === null) return '星级必须是数字'
  if (star < options.limits.minStar) return `星级不能小于 ${options.limits.minStar}（服务端会把 0 补回该装备的基础星级）`
  if (star > options.limits.maxStar) return `星级不能超过 ${options.limits.maxStar}`
  const quality = integerField(draft.quality)
  if (quality === null) return '装备品质必须是数字'
  if (quality < options.limits.minQuality) return `装备品质不能小于 ${options.limits.minQuality}`
  if (quality > options.limits.maxQuality) return `装备品质不能超过 ${options.limits.maxQuality}`

  const rows = draft.randomRows.filter(Boolean)
  if (rows.length !== star) return `星级 ${star} 需要 ${star} 条随机属性，当前选了 ${rows.length} 条`
  if (new Set(rows).size !== rows.length) return '随机属性不能重复'
  const randomPool = new Set(manualRandomPool(options, quality).map(attr => String(attr.id)))
  for (const id of rows) {
    if (!randomPool.has(id)) return `随机属性 ${manualRandomById(options, id)?.keyName || id} 不属于品质 ${quality} 的属性池`
  }
  return ''
}

// equipManualPayload 与服务端 gmEquipManualPayload 对齐。
//
// 定位用 location + slotIndex（player_items 主键）；serverId 只在非 0 时带上，
// 因为服务端把 serverId 当正实例号校验，传 0 会直接「装备实例不能小于 1」。
//
// 服务端每个字段都是「提交了才改」（全部是指针），所以这里只发这三件事，
// 强化 / 词缀 / 宝石一概不带，也就不会被那三套规则牵连着拒单。
export function equipManualPayload(draft: EquipManualDraft, equip: EquipManualEquipOption | null) {
  return {
    ...equipTarget(equip?.location, equip?.slotIndex, equip?.serverId),
    star: draft.star.trim(),
    quality: draft.quality.trim(),
    randomAttrs: draft.randomRows.filter(Boolean).map(Number),
  }
}

export function EquipManualPanel({ draft, onChange, options, equipOptions, disabled }: {
  draft: EquipManualDraft
  onChange: (next: EquipManualDraft) => void
  options: EquipManualOptions
  equipOptions: EquipManualEquipOption[]
  disabled: boolean
}) {
  const patch = (values: Partial<EquipManualDraft>) => onChange({ ...draft, ...values })
  const star = integerField(draft.star) ?? 0
  const quality = manualQuality(options, draft.quality)
  const randomPool = manualRandomPool(options, quality)

  const changeStar = (value: string) => {
    // 星级 ↔ 随机属性条数强制联动：多退少补，新增行默认取该品质档位里还没用过的属性。
    patch({ star: value, randomRows: resizeRowIds(draft.randomRows, integerField(value) ?? 0, randomPool.map(attr => String(attr.id))) })
  }
  const changeQuality = (value: string) => {
    const nextQuality = manualQuality(options, value)
    // 档位一变，原来挑的随机属性就全废了（服务端按 id/100 == 品质 卡），整体重填。
    patch({ quality: value, randomRows: resizeRowIds([], star, manualRandomPool(options, nextQuality).map(attr => String(attr.id))) })
  }

  return <>
    <div className="notice">手工装备是「直接设置装备的最终状态」：不消耗金币和强化石，也不重新生成主属性；随机属性的数值由配置表决定，只能挑不能填。星级最低 {options.limits.minStar}：填 0 会被服务端在下次登录按装备基础星级补回来。宝石、洗练词缀、强化各有独立入口，这里不碰。</div>
    <Field label="装备" hint="含背包、身上和仓库，按「位置 + 槽位」定位">
      <select className="select" value={draft.equipKey} disabled={disabled} onChange={event => onChange({ ...draft, equipKey: event.target.value })}>
        {equipOptions.map(item => <option key={item.key} value={item.key}>{item.label}</option>)}
        {equipOptions.length ? null : <option value="">暂无装备</option>}
      </select>
    </Field>
    <div className="section-title">星级 / 品质 / 随机属性</div>
    <div className="field-row">
      <Field label={`星级 ${options.limits.minStar}-${options.limits.maxStar}`} hint={`星级 N 就必须正好 N 条随机属性（当前 ${star} 条）`}><input className="input" inputMode="numeric" value={draft.star} disabled={disabled} onChange={event => changeStar(event.target.value)} /></Field>
      <Field label={`品质 ${options.limits.minQuality}-${options.limits.maxQuality}`} hint="决定随机属性的档位"><input className="input" inputMode="numeric" value={draft.quality} disabled={disabled} onChange={event => changeQuality(event.target.value)} /></Field>
    </div>
    <div className="notice">随机属性只能从品质 {quality} 的候选池里挑（共 {randomPool.length} 条），id 的档位由品质唯一决定，改品质会把已挑的整组作废重填。</div>
    {draft.randomRows.map((row, index) => (
      <div className="field-row" key={index}>
        <Field label={index === 0 ? '随机属性' : ''}>
          <select className="select" value={row} disabled={disabled} onChange={event => patch({ randomRows: draft.randomRows.map((value, itemIndex) => itemIndex === index ? event.target.value : value) })}>
            <option value="">选择属性</option>
            {randomPool.map(attr => <option key={attr.id} value={String(attr.id)} disabled={draft.randomRows.some((value, itemIndex) => itemIndex !== index && value === String(attr.id))}>{attr.keyName} +{attr.value}</option>)}
          </select>
        </Field>
        <Field label={index === 0 ? '数值' : ''}><input className="input" readOnly value={manualRandomById(options, row)?.value ?? ''} disabled /></Field>
      </div>
    ))}
  </>
}
