import { useQuery } from '@tanstack/react-query'
import { Check, ChevronLeft, ChevronRight, Download, Plus, Search, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { api, CatalogItem } from '../api/client'
import { Button, ErrorState, Field, LoadingState } from './Ui'
import './ItemPicker.css'

export type SelectedItem = { item: CatalogItem; count: string }
const maxItems = 100
const maxCount = 1_000_000_000
const pageSize = 8

export function itemSelectionError(items: SelectedItem[], required: boolean) {
  if (required && !items.length) return '请搜索并选择至少一种物品'
  if (items.length > maxItems) return `每次最多选择 ${maxItems} 种物品`
  const invalid = items.find(({ count }) => !/^\d+$/.test(count) || Number(count) < 1 || Number(count) > maxCount)
  return invalid ? `${invalid.item.name}的数量须为 1 至 ${maxCount.toLocaleString('zh-CN')} 的整数` : ''
}

export function itemSelectionPayload(items: SelectedItem[]) {
  return items.map(({ item, count }) => ({ itemId: item.id, count: Number(count) }))
}

export function ItemPicker({ items, onChange, disabled = false, optional = false }: {
  items: SelectedItem[]; onChange: (items: SelectedItem[]) => void; disabled?: boolean; optional?: boolean
}) {
  const catalog = useQuery({ queryKey: ['item-catalog'], queryFn: api.items, staleTime: 30_000 })
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('')
  const [job, setJob] = useState('')
  const [slot, setSlot] = useState('')
  const [page, setPage] = useState(0)
  const all = catalog.data?.items
  const categories = useMemo(() => [...new Set(all?.map(item => item.category) || [])], [all])
  const jobs = useMemo(() => [...new Map(all?.map(item => [item.jobType, item.jobName]) || []).entries()].sort((a, b) => a[0] - b[0]), [all])
  const slots = useMemo(() => [...new Map(all?.filter(item => item.itemType === 1).map(item => [item.equipSlot, item.equipSlotName]) || []).entries()].sort((a, b) => a[0] - b[0]), [all])
  const filtered = useMemo(() => {
    const query = search.trim().toLocaleLowerCase('zh-CN')
    return (all || []).filter(item => (!category || item.category === category) &&
      (job === '' || item.jobType === 0 || item.jobType === Number(job)) &&
      (slot === '' || (item.itemType === 1 && item.equipSlot === Number(slot))) &&
      (!query || item.name.toLocaleLowerCase('zh-CN').includes(query) || String(item.id).includes(query) || (item.equipSlotName || '').includes(query)))
  }, [all, category, job, slot, search])
  const pages = Math.max(1, Math.ceil(filtered.length / pageSize))
  const currentPage = Math.min(page, pages - 1)
  const selected = new Set(items.map(entry => entry.item.id))
  const add = (item: CatalogItem) => {
    if (!disabled && !selected.has(item.id) && items.length < maxItems) onChange([...items, { item, count: '1' }])
  }
  return <section className="item-picker" aria-label={optional ? '邮件附件选择' : '发放物品选择'}>
    <div className="item-picker-heading">
      <h3>{optional ? '邮件附件（选填）' : '选择物品'}</h3>
      <a className="link item-export" href="/api/v1/items/export" download><Download size={14} aria-hidden="true" />下载中文清单</a>
    </div>
    <p className="item-picker-help">搜索中文名称或 ID，点击物品即可添加，再填写数量。{optional ? '不选附件也可发送邮件。' : ''}</p>
    <div className="item-search-row">
      <Field label="搜索物品">
        <div className="item-search-input"><Search size={16} aria-hidden="true" /><input className="input" type="search" placeholder="如：强化、宝石、110305" value={search} disabled={disabled} onChange={event => { setSearch(event.target.value); setPage(0) }} onKeyDown={event => { if (event.key === 'Enter') event.preventDefault() }} /></div>
      </Field>
      <Field label="物品分类"><select className="select" value={category} disabled={disabled} onChange={event => { setCategory(event.target.value); setSlot(''); setPage(0) }}><option value="">全部分类</option>{categories.map(value => <option key={value} value={value}>{value}</option>)}</select></Field>
      <Field label="装备部位"><select className="select" value={slot} disabled={disabled || (category !== '' && category !== '装备')} onChange={event => { setSlot(event.target.value); setPage(0) }}><option value="">全部部位</option>{slots.map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></Field>
      <Field label="适用职业"><select className="select" value={job} disabled={disabled} onChange={event => { setJob(event.target.value); setPage(0) }}><option value="">全部职业</option>{jobs.map(([id, name]) => <option key={id} value={id}>{id === 0 ? '仅通用物品' : `${name}（含通用）`}</option>)}</select></Field>
    </div>
    {catalog.isLoading ? <LoadingState /> : catalog.isError ? <ErrorState message={catalog.error instanceof Error ? catalog.error.message : undefined} onRetry={() => catalog.refetch()} /> : <>
      <div className="item-results-caption" role="status">找到 {filtered.length} 个物品 · 清单共 {catalog.data?.total || 0} 个</div>
      {filtered.length ? <ul className="item-results" aria-label="物品搜索结果">
        {filtered.slice(currentPage * pageSize, (currentPage + 1) * pageSize).map(item => <li key={item.id}>
          <button type="button" className="item-result" disabled={disabled || selected.has(item.id) || items.length >= maxItems} onClick={() => add(item)} aria-label={`${selected.has(item.id) ? '已选择' : '添加'} ${item.name}（${item.id}）`}>
            <span className="item-result-text"><strong>{item.name}</strong><span className="item-result-meta"><span className="mono">{item.id}</span><span>{item.equipSlotName || item.category} · {item.jobName}</span>{item.level > 0 ? <span>{item.level.toLocaleString('zh-CN')} 级</span> : null}</span></span>
            <span className="item-result-action">{selected.has(item.id) ? <><Check size={15} aria-hidden="true" />已选</> : <><Plus size={15} aria-hidden="true" />添加</>}</span>
          </button>
        </li>)}
      </ul> : <div className="item-no-results">没有匹配的物品，请尝试更短的名称、数字 ID 或选择“全部分类”。</div>}
      {pages > 1 ? <div className="item-pagination"><Button type="button" variant="ghost" disabled={disabled || currentPage === 0} onClick={() => setPage(currentPage - 1)} aria-label="上一页物品"><ChevronLeft size={16} aria-hidden="true" />上一页</Button><span>{currentPage + 1} / {pages}</span><Button type="button" variant="ghost" disabled={disabled || currentPage >= pages - 1} onClick={() => setPage(currentPage + 1)} aria-label="下一页物品">下一页<ChevronRight size={16} aria-hidden="true" /></Button></div> : null}
    </>}
    <div className="item-selection-heading" role="status">已选 {items.length} 种{optional ? '附件' : '物品'}{items.length >= maxItems ? `（已达 ${maxItems} 种上限）` : ''}</div>
    {!items.length ? <div className="item-selection-empty">{optional ? '暂无附件，点击上方物品添加。' : '点击上方物品加入发放列表。'}</div> : <ul className="item-selection" aria-label="已选物品">
      {items.map(({ item, count }, index) => <li key={item.id} className="item-selected">
        <div className="item-selected-main"><strong>{item.name}</strong><span className="mono muted">ID {item.id} · {item.equipSlotName || item.category} · {item.jobName}</span>{item.description ? <p>{item.description}</p> : null}{item.delivery ? <p className="item-delivery">{item.delivery}</p> : null}</div>
        <Field label="数量"><input className="input" type="number" inputMode="numeric" aria-label={`${item.name}（${item.id}）数量`} min={1} max={maxCount} step={1} required value={count} disabled={disabled} onChange={event => onChange(items.map((entry, i) => i === index ? { ...entry, count: event.target.value } : entry))} /></Field>
        <button type="button" className="icon-btn item-remove" disabled={disabled} aria-label={`移除 ${item.name}（${item.id}）`} onClick={() => onChange(items.filter(entry => entry.item.id !== item.id))}><Trash2 size={17} aria-hidden="true" /></button>
      </li>)}
    </ul>}
  </section>
}
