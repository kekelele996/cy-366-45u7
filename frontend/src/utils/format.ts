export function formatTime(value?: string | null): string {
  if (!value) return '-'
  return value.replace('T', ' ').slice(0, 19)
}

function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

// dateToRFC3339 将本地 Date 转为后端 time.Time 可解析的 RFC3339（带时区偏移）。
export function dateToRFC3339(d: Date): string {
  const offsetMin = -d.getTimezoneOffset()
  const sign = offsetMin >= 0 ? '+' : '-'
  const oh = pad2(Math.floor(Math.abs(offsetMin) / 60))
  const om = pad2(Math.abs(offsetMin) % 60)
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}${sign}${oh}:${om}`
}

// toRFC3339 将 "YYYY-MM-DD HH:mm:ss"（本地时间）转为 RFC3339；无法解析时原样返回。
export function toRFC3339(value: string): string {
  const d = new Date(value.replace(/-/g, '/'))
  if (Number.isNaN(d.getTime())) return value
  return dateToRFC3339(d)
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
