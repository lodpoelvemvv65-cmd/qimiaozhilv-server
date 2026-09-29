// 金币/银/铜的换算率与游戏客户端 TabHelper.GetCoinFormat 一致：
// 100 铜 = 1 银，10000 铜 = 1 金（文档/13 货币 NumericType）。
const COPPER_PER_SILVER = 100
const COPPER_PER_GOLD = 10000

const decimal = new Intl.NumberFormat('zh-CN')

/**
 * 把服务端返回的铜币余额格式化成金/银/铜三位，和游戏里显示的一致。
 *
 * players.coin 存的是铜，不是金：原始值 100000 表示 10 金，直接当金币展示
 * 会被读成 10 万金，差 10000 倍。整金只显示金，有零头才补银和铜，避免列表
 * 里塞满无意义的“0 银 0 铜”。
 */
export function formatCoin(value: unknown): string {
  const total = Number(value ?? 0)
  if (!Number.isFinite(total)) return '—'
  const abs = Math.abs(Math.trunc(total))
  const gold = Math.floor(abs / COPPER_PER_GOLD)
  const silver = Math.floor((abs % COPPER_PER_GOLD) / COPPER_PER_SILVER)
  const copper = abs % COPPER_PER_SILVER
  const parts = [`${decimal.format(gold)} 金`]
  if (silver > 0) parts.push(`${silver} 银`)
  if (copper > 0) parts.push(`${copper} 铜`)
  return (total < 0 ? '-' : '') + parts.join(' ')
}

/**
 * 铜币余额的原始铜值，用于 title 悬浮提示。金/银/铜三位会隐藏不足 1 金的
 * 零头之外的信息，对账时还是得看数据库里的精确整数。
 */
export function formatCoinCopper(value: unknown): string {
  const total = Number(value ?? 0)
  return Number.isFinite(total) ? decimal.format(total) : '—'
}
