import { Trash2 } from 'lucide-react'
import { Button, Field } from '../../components/Ui'
import { EquipManualOptions } from '../../api/client'
import { EquipManualEquipOption, equipTarget, manualAffixById, manualAffixOptions, manualBonusCount, manualQuality } from './EquipManualPanel'

// 洗练词缀面板（GM action player.equip_affix）。
//
// 与「手工装备」是两个独立入口：词缀是装备上一个独立的操作面，给一件装备换词缀
// 不该被迫一起提交星级和随机属性。服务端也是单独的 action，规则实现共用同一份
// （gmResolveManualAddAttrs），见 server-mysql/gm_control_equip_affix.go。
//
// 语义是「设置这件装备洗练词缀的最终状态」：不消耗金币、不消耗洗练道具。
//
// 只有三种状态能稳定持久，这是整个面板存在的理由——equip.go 的
// ensureEquipmentAffixCount 在**每次** encodeEquipTrans（即每一次背包推送）和登录
// 修复里都会跑，它会丢弃不在 affixPoolForQuality(品质) 里的 id、去重，然后**从池子里
// 按序补齐**到 equipmentBonusCount(品质) 条：
//   - 空列表：被 `len > 0` 守卫跳过，稳定；
//   - 正好 bonusCount(品质) 条（白绿 1 / 蓝紫 2 / 橙红 3）且都在该品质池内：稳定；
//   - 1 条六维（含满六维）：整件跳过修复，稳定。
// 中间态（比如红装只给 1 条普通词缀）重登必被改写成 3 条，所以服务端直接拒绝，
// 面板也要在提交前就把这个数字摆出来。
//
// 品质不由这里设：词缀池和槽位数都跟着装备**当前**品质走，改品质请去「手工装备」。

export type EquipAffixDraft = {
  equipKey: string // `${location}|${slotIndex}`
  affixRows: string[]
}

export const emptyEquipAffixDraft: EquipAffixDraft = { equipKey: '', affixRows: [] }

// equipAffixError 镜像服务端 gmResolveManualAddAttrs 的判定，只为让运营在点
// 「确认执行」之前就看到同一个数字。服务端 gm_control_equip_affix.go 的文案是最终口径。
export function equipAffixError(draft: EquipAffixDraft, options: EquipManualOptions, quality: number): string {
  if (!draft.equipKey) return '请选择要调整词缀的装备'
  const rows = draft.affixRows.filter(Boolean)
  if (new Set(rows).size !== rows.length) return '洗练词缀不能重复'
  if (rows.length === 0) return ''
  const sixDimension = rows.filter(id => manualAffixById(options, id)?.sixDimension)
  if (sixDimension.length > 0) {
    // 含六维时服务端整件跳过登录修复，所以只要条数对就能稳定。
    if (rows.length !== 1) return `六维词缀只能 1 条，当前选了 ${rows.length} 条`
    return ''
  }
  const target = manualBonusCount(options, quality)
  if (target <= 0) return `品质 ${quality} 没有可用的词缀池，无法设置洗练词缀`
  if (rows.length !== target) {
    return `品质 ${quality} 的洗练词缀只能为空或正好 ${target} 条，当前选了 ${rows.length} 条（不足会被服务端按词缀池补齐，多余会被裁掉）`
  }
  const pool = new Set((options.affixPools.byQuality[String(quality)] || []).map(String))
  for (const id of rows) {
    if (!pool.has(id)) return `洗练词缀「${manualAffixById(options, id)?.label || id}」不在品质 ${quality} 的词缀池内，重登会被服务端改写`
  }
  return ''
}

// equipAffixPayload 与服务端 gmEquipAffixPayload 对齐：只有 addAttrs 一个业务字段。
// 空数组是合法提交值，意思是「清空词缀」，所以必须原样发出去，不能省掉。
export function equipAffixPayload(draft: EquipAffixDraft, equip: EquipManualEquipOption | null) {
  return {
    ...equipTarget(equip?.location, equip?.slotIndex, equip?.serverId),
    addAttrs: draft.affixRows.filter(Boolean).map(Number),
  }
}

export function EquipAffixPanel({ draft, onChange, options, quality, equipOptions, disabled }: {
  draft: EquipAffixDraft
  onChange: (next: EquipAffixDraft) => void
  options: EquipManualOptions
  quality: number
  equipOptions: EquipManualEquipOption[]
  disabled: boolean
}) {
  // 品质取的是装备当前落库的品质；候选池也跟着它变。
  const effectiveQuality = manualQuality(options, String(quality))
  const affixList = manualAffixOptions(options, effectiveQuality)
  const target = manualBonusCount(options, effectiveQuality)
  const rows = draft.affixRows.filter(Boolean)
  const sixDimension = rows.some(id => manualAffixById(options, id)?.sixDimension)

  return <>
    <div className="notice">调整词缀是「直接设置装备洗练词缀的最终状态」：不消耗金币，也不消耗洗练道具。整组提交，空着提交就是「清空词缀」。</div>
    <Field label="装备" hint="含背包、身上和仓库，按「位置 + 槽位」定位">
      <select className="select" value={draft.equipKey} disabled={disabled} onChange={event => onChange({ ...draft, equipKey: event.target.value })}>
        {equipOptions.map(item => <option key={item.key} value={item.key}>{item.label}</option>)}
        {equipOptions.length ? null : <option value="">暂无装备</option>}
      </select>
    </Field>
    <div className="section-title">洗练词缀（当前 {rows.length} / {target} 条）</div>
    {/* 装备品质决定词缀池和槽位数，这里只读展示：改品质要去「手工装备」。 */}
    <div className="notice">这件装备的品质是 {effectiveQuality}，词缀池共 {affixList.length} 条，槽位 {target} 个。品质决定两者，改品质请到「手工装备」面板。</div>
    {draft.affixRows.map((row, index) => (
      <div className="field-row" key={index}>
        <Field label={index === 0 ? '洗练词缀' : ''}>
          <select className="select" value={row} disabled={disabled} onChange={event => onChange({ ...draft, affixRows: draft.affixRows.map((value, itemIndex) => itemIndex === index ? event.target.value : value) })}>
            <option value="">选择词缀</option>
            {affixList.map(affix => <option key={affix.id} value={String(affix.id)} disabled={draft.affixRows.some((value, itemIndex) => itemIndex !== index && value === String(affix.id))}>{affix.label}{affix.sixDimension ? '（六维，只能 1 条）' : ''}</option>)}
          </select>
        </Field>
        <Field label={index === 0 ? '操作' : ''}>
          <Button type="button" variant="secondary" disabled={disabled} onClick={() => onChange({ ...draft, affixRows: draft.affixRows.filter((_, itemIndex) => itemIndex !== index) })}><Trash2 size={15} />移除</Button>
        </Field>
      </div>
    ))}
    <div style={{ display: 'flex', gap: 10, marginTop: 4 }}>
      {/* 六维只能 1 条，已经挑了六维就不让再加行，避免拼出一个必被拒的组合。 */}
      <Button type="button" variant="secondary" disabled={disabled || draft.affixRows.length >= Math.max(target, 1) || sixDimension} onClick={() => onChange({ ...draft, affixRows: [...draft.affixRows, ''] })}>增加词缀</Button>
      <Button type="button" variant="secondary" disabled={disabled || draft.affixRows.length === 0} onClick={() => onChange({ ...draft, affixRows: [] })}>清空词缀</Button>
    </div>
    <div className="notice">只有三种状态能稳定持久（重登不变样）：空、正好 {target} 条（品质 {effectiveQuality} 的槽位）、或 1 条六维。中间态重登会被服务端按词缀池补齐或裁掉，服务端会直接拒单。</div>
  </>
}
