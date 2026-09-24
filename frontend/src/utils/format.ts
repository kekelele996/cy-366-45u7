export function formatTime(value?: string | null): string {
  if (!value) return '-'
  return value.replace('T', ' ').slice(0, 19)
}

// toRFC3339 将 "YYYY-MM-DD HH:mm:ss" 本地时间转为带时区偏移的 RFC3339（后端 time.Time 仅接受该格式）。
export function toRFC3339(local: string): string {
  if (!local) return ''
  const iso = local.replace(' ', 'T')
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return local
  const offsetMin = -d.getTimezoneOffset()
  const sign = offsetMin >= 0 ? '+' : '-'
  const abs = Math.abs(offsetMin)
  const hh = String(Math.floor(abs / 60)).padStart(2, '0')
  const mm = String(abs % 60).padStart(2, '0')
  return `${iso}${sign}${hh}:${mm}`
}

export function formatMoney(value?: number | null): string {
  if (value === null || value === undefined) return '¥0.00'
  return `¥${Number(value).toFixed(2)}`
}

export function formatDuration(minutes?: number | null): string {
  if (!minutes) return '0分钟'
  const m = Number(minutes)
  if (m < 60) return `${m}分钟`
  return `${Math.floor(m / 60)}小时${m % 60}分钟`
}
