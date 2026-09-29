import { useEffect } from 'react'
import { Button, Field } from '../../components/Ui'
import { EquipManualEquipRule, EquipManualGem, EquipManualOptions } from '../../api/client'
import { EquipManualEquipOption, equipTarget, manualGemById } from './EquipManualPanel'

// 宝石镶嵌面板（GM action player.equip_gem）。
//
// 和「手工装备」是**两个独立入口**：宝石是装备上一个独立的操作面，不隶属手工打造，
// 给一件普通装备换宝石不该被迫走一遍星级 / 随机属性 / 洗练词缀的校验。
// 服务端也是分开的两个 action，规则实现共用同一份（gem.go 的
// gemConfig / equipAllowsGem / equipMaxHole），见 server-mysql/gm_control_equip_gem.go。
//
// 交互：每个宝石孔「先选等级、再选宝石」。一件武器允许的宝石可能有 70 多颗，
// 平铺成一个下拉根本挑不动；按等级收窄之后每级通常只剩十来个候选。
// 等级是**每个孔各自**选的，四个孔想混用不同等级不用来回切；换了等级会把该孔
// 原来那颗等级对不上的宝石清掉，避免「等级显示二级、实际镶的是一级」。
//
// 语义是「设置这件装备宝石槽的最终状态」，不是「模拟一次镶嵌动作」：
//   - 提交的是定长 maxHole 项（空槽 0），镶嵌 / 替换 / 整件卸下都走同一个入口；
//   - 不扣金币、不消耗背包里的宝石；
//   - 装备锁定时服务端整单拒绝，这里直接拦住并说明去哪儿解锁。
//
// 定位方式与手工装备一致：location + slotIndex，不用恒为 0 的 server_id。

export type EquipGemSlot = { level: string; gem: string }
export type EquipGemDraft = {
  equipKey: string // `${location}|${slotIndex}`
  slots: EquipGemSlot[]
}

export const emptyEquipGemDraft: EquipGemDraft = { equipKey: '', slots: [] }

// gemLevelOf 把缺失 / 非法的 GemLevel 归成 0 档（「无等级」）。配置表里宝石都有
// 等级，但候选池是通用接口，不能让一颗缺字段的宝石把整个下拉搞崩。
export function gemLevelOf(gem: EquipManualGem | undefined): number {
  const value = Number(gem?.gemLevel)
  return Number.isFinite(value) && value > 0 ? value : 0
}

// gemLevelLabel 只用于显示。1..10 用中文数字（与物品名里的「·一级」一致），
// 超出范围退回「N 级」，0 档叫「无等级」。
export function gemLevelLabel(level: unknown): string {
  const value = Number(level)
  if (!Number.isFinite(value) || value <= 0) return '无等级'
  const digits = ['一', '二', '三', '四', '五', '六', '七', '八', '九', '十']
  return value <= digits.length ? `${digits[value - 1]}级` : `${value} 级`
}

// allowedGemLevels 是这件装备允许镶嵌的宝石等级（升序）。等级下拉只列这些，
// 运营不会选到一个下面一颗宝石都没有的空等级。
export function allowedGemLevels(options: EquipManualOptions, rule: EquipManualEquipRule | null): number[] {
  const allowed = new Set((rule?.allowedGemIds || []).map(String))
  const levels = new Set<number>()
  for (const gem of options.gems) {
    if (allowed.has(String(gem.id))) levels.add(gemLevelOf(gem))
  }
  return [...levels].sort((left, right) => left - right)
}

// gemsAtLevel 是该孔当前等级下的候选。同一属性（GemKey）已经在别的孔镶了就不列，
// 与服务端「该装备已镶嵌相同属性宝石，请更换其他属性」保持一致。
export function gemsAtLevel(options: EquipManualOptions, rule: EquipManualEquipRule | null, level: string): EquipManualGem[] {
  const allowed = new Set((rule?.allowedGemIds || []).map(String))
  return options.gems.filter(gem => allowed.has(String(gem.id)) && String(gemLevelOf(gem)) === String(level))
}

// resizeGemSlots 把孔位数组调整到 count 个：多则截断，少则按默认等级补空槽。
export function resizeGemSlots(slots: EquipGemSlot[], count: number, defaultLevel: string): EquipGemSlot[] {
  const next = slots.slice(0, Math.max(count, 0))
  while (next.length < count) next.push({ level: defaultLevel, gem: '' })
  return next
}

// equipGemError 镜像服务端 gmResolveManualGems 的宝石判定，只为让运营在点「确认执行」
// 之前就看到同一个数字。服务端 gm_control_equip_gem.go 的文案才是最终口径。
export function equipGemError(draft: EquipGemDraft, options: EquipManualOptions, rule: EquipManualEquipRule | null, locked: boolean): string {
  if (!draft.equipKey) return '请选择要镶嵌宝石的装备'
  if (locked) return '这件装备已锁定，无法镶嵌；请先到「调整装备」面板解锁后再来镶嵌'
  const maxHole = rule?.maxHole ?? 0
  if (maxHole <= 0) return '该装备没有宝石槽（EquipBase.MaxHole 为 0），无法镶嵌'
  if (draft.slots.length !== maxHole) return `该装备有 ${maxHole} 个宝石槽，需要正好 ${maxHole} 项`
  const allowed = new Set((rule?.allowedGemIds || []).map(String))
  const seenKeys = new Map<number, number>()
  for (let slot = 0; slot < draft.slots.length; slot++) {
    const id = draft.slots[slot].gem
    if (!id) continue
    const gem = manualGemById(options, id)
    if (!gem) return `第 ${slot + 1} 个宝石孔的物品不在宝石表里`
    if (!allowed.has(id)) return `第 ${slot + 1} 个宝石孔不能镶嵌「${gem.name}」（${gem.gemKeyName} 这种属性该部位不支持）`
    // 等级只是前端用来收窄候选的过滤器，改了等级却没换宝石属于「显示与实际不符」，
    // 拦在这里比让服务端存下一个 UI 上看不出来的值要好。
    if (String(gemLevelOf(gem)) !== String(draft.slots[slot].level)) {
      return `第 ${slot + 1} 个宝石孔选的是${gemLevelLabel(draft.slots[slot].level)}，但「${gem.name}」是${gemLevelLabel(gemLevelOf(gem))}`
    }
    const previous = seenKeys.get(gem.gemKey)
    if (previous !== undefined) {
      return `第 ${previous + 1} 与第 ${slot + 1} 个宝石孔同为属性「${gem.gemKeyName}」，同一属性不能镶嵌两颗`
    }
    seenKeys.set(gem.gemKey, slot)
  }
  return ''
}

// equipGemPayload 与服务端 gmEquipGemPayload 对齐。宝石槽必须定长发满 maxHole 项
// （空槽 0）：服务端要按 EquipBase.MaxHole 校验长度，缺项会被整单拒绝。
export function equipGemPayload(draft: EquipGemDraft, rule: EquipManualEquipRule | null, locked: boolean, equip: EquipManualEquipOption | null) {
  const maxHole = rule?.maxHole ?? 0
  return {
    ...equipTarget(equip?.location, equip?.slotIndex, equip?.serverId),
    ...(maxHole > 0 && !locked ? { gems: draft.slots.map(slot => (slot.gem ? Number(slot.gem) : 0)) } : {}),
  }
}

export function EquipGemPanel({ draft, onChange, options, rule, locked, equipOptions, disabled }: {
  draft: EquipGemDraft
  onChange: (next: EquipGemDraft) => void
  options: EquipManualOptions
  rule: EquipManualEquipRule | null
  locked: boolean
  equipOptions: EquipManualEquipOption[]
  disabled: boolean
}) {
  const maxHole = rule?.maxHole ?? 0
  const levels = allowedGemLevels(options, rule)
  const defaultLevel = levels.length ? String(levels[0]) : ''
  const filled = draft.slots.filter(slot => slot.gem).length
  const allowedGemIds = (rule?.allowedGemIds || []).map(String)
  // 全量候选（只按部位过滤），用来判断某颗宝石属于哪一级、以及给「不在当前等级里」
  // 的历史值兜底显示。
  const allAllowed = options.gems.filter(gem => allowedGemIds.includes(String(gem.id)))
  const levelKeys = levels.join(',')

  // 孔位数必须和 MaxHole 一样（服务端要求正好 maxHole 项，空槽发 0）。
  // equipRules 是选中装备后才回来的，所以槽位数在这里补齐，而不是在回填时。
  useEffect(() => {
    if (maxHole <= 0 || draft.slots.length === maxHole) return
    onChange({ ...draft, slots: resizeGemSlots(draft.slots, maxHole, defaultLevel) })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [maxHole, draft.slots.length])

  // 空槽也要有个选得中的等级：回填和解锁之后可能留下 level 为空的槽，
  // 统一落到该部位允许的最低等级，免得下拉显示空白。levels 还没到就直接跳过。
  useEffect(() => {
    if (!levels.length || !draft.slots.some(slot => !slot.level)) return
    onChange({ ...draft, slots: draft.slots.map(slot => (slot.level ? slot : { ...slot, level: defaultLevel })) })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [levelKeys, draft.slots])

  const changeLevel = (index: number, level: string) => {
    const current = manualGemById(options, draft.slots[index].gem)
    // 新等级下拉里没有这颗宝石就把该孔清空，避免「等级写着二级、实际镶的是一级」。
    const keep = current && String(gemLevelOf(current)) === level
    onChange({
      ...draft,
      slots: draft.slots.map((slot, itemIndex) => itemIndex === index ? { level, gem: keep ? slot.gem : '' } : slot),
    })
  }

  return <>
    <div className="notice">宝石镶嵌是「直接设置装备宝石槽的最终状态」：不消耗金币，也不消耗背包里的宝石。每个孔先选等级、再选宝石；空槽留「不使用」就等于卸下，整件清掉用「全部卸下」。</div>
    <Field label="装备" hint="含背包、身上和仓库，按「位置 + 槽位」定位">
      <select className="select" value={draft.equipKey} disabled={disabled} onChange={event => onChange({ ...draft, equipKey: event.target.value })}>
        {equipOptions.map(item => <option key={item.key} value={item.key}>{item.label}</option>)}
        {equipOptions.length ? null : <option value="">暂无装备</option>}
      </select>
    </Field>
    <div className="section-title">宝石孔（已镶嵌 {filled} / {maxHole}）</div>
    {locked
      ? <div className="notice danger">这件装备已锁定，服务端会拒绝任何宝石改动。请先到「调整装备」面板把它改成「未锁定」，再回来镶嵌。</div>
      : maxHole > 0
        ? <>
          <div className="notice">宝石孔按该装备的部位规则过滤（部位 {rule?.typeName}，共 {maxHole} 个孔）：只能挑该部位允许的属性，且同一属性不能镶嵌两颗。可选等级 {levels.map(gemLevelLabel).join(' / ')}。</div>
          {draft.slots.map((slot, index) => {
            const candidates = gemsAtLevel(options, rule, slot.level)
            const selected = manualGemById(options, slot.gem)
            // 该孔已经镶着的宝石如果不在当前等级的候选里（换了等级还没重选、或这件装备
            // 的部位规则变了），要把它作为额外选项挂上，否则下拉会显示成别的宝石。
            const orphan = slot.gem && !candidates.some(gem => String(gem.id) === slot.gem)
            return <div className="field-row" key={index}>
              <Field label={`宝石孔 ${index + 1} 等级`} hint={index === 0 ? '每个孔各自选等级' : undefined}>
                <select className="select" value={slot.level} disabled={disabled} onChange={event => changeLevel(index, event.target.value)}>
                  {levels.map(level => <option key={level} value={String(level)}>{gemLevelLabel(level)}</option>)}
                  {/* 等级为 0（无等级）或不在允许列表里时补一个，保证 value 一定选得中 */}
                  {levels.some(level => String(level) === slot.level) ? null : <option value={slot.level}>{gemLevelLabel(slot.level)}</option>}
                </select>
              </Field>
              <Field label="宝石" hint={index === 0 ? `只列「${gemLevelLabel(slot.level)}」的宝石，共 ${candidates.length} 颗` : undefined}>
                <select className="select" value={slot.gem} disabled={disabled} onChange={event => onChange({ ...draft, slots: draft.slots.map((value, itemIndex) => itemIndex === index ? { ...value, gem: event.target.value } : value) })}>
                  <option value="">不使用（空槽 = 卸下）</option>
                  {orphan ? <option value={slot.gem}>{selected ? `${selected.name}（${selected.gemKeyName} · ${selected.gemTypeName}）` : `未知宝石 ${slot.gem}`}</option> : null}
                  {candidates.map(gem => <option key={gem.id} value={String(gem.id)} disabled={draft.slots.some((value, itemIndex) => itemIndex !== index && value.gem !== '' && manualGemById(options, value.gem)?.gemKey === gem.gemKey)}>{gem.name}（{gem.gemKeyName} · {gem.gemTypeName}）</option>)}
                </select>
              </Field>
            </div>
          })}
          <div style={{ display: 'flex', gap: 10, marginTop: 4 }}>
            <Button type="button" variant="secondary" disabled={disabled || filled === 0} onClick={() => onChange({ ...draft, slots: draft.slots.map(slot => ({ ...slot, gem: '' })) })}>全部卸下</Button>
          </div>
          {allAllowed.length === 0 ? <div className="notice danger" role="alert">这件装备允许的宝石一颗都没匹配到配置表，请检查 GemInlayConfig 与 MaterialBase。</div> : null}
        </>
        : <div className="notice">该装备没有宝石槽（EquipBase.MaxHole 为 0），无法镶嵌。称号类装备在原版表里就是 0 孔。</div>}
  </>
}

// gemHoleLabel 给成功回执用的一句摘要，避免各处重复拼字符串。
export function gemHoleLabel(gems: unknown): string {
  const list = Array.isArray(gems) ? gems : []
  return `${list.filter(id => Number(id) > 0).length} / ${list.length} 个宝石孔`
}
