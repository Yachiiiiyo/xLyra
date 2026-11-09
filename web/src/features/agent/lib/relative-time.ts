const DIVISIONS: Array<{ limit: number; divisor: number; unit: Intl.RelativeTimeFormatUnit }> = [
  { limit: 60, divisor: 1, unit: 'second' },
  { limit: 3600, divisor: 60, unit: 'minute' },
  { limit: 86400, divisor: 3600, unit: 'hour' },
  { limit: 2629800, divisor: 86400, unit: 'day' },
  { limit: 31557600, divisor: 2629800, unit: 'month' },
  { limit: Infinity, divisor: 31557600, unit: 'year' },
]

export function formatRelativeTime(value: string | number | undefined, locale: string, now = Date.now()): string {
  if (value === undefined || value === null || value === '') return ''
  const timestamp = new Date(value).getTime()
  if (!Number.isFinite(timestamp)) return ''
  const diffSeconds = Math.round((timestamp - now) / 1000)
  const absSeconds = Math.abs(diffSeconds)
  const division = DIVISIONS.find((item) => absSeconds < item.limit) ?? DIVISIONS[DIVISIONS.length - 1]
  const language = locale.startsWith('ja') || locale.startsWith('jp') ? 'ja' : locale.startsWith('en') ? 'en' : 'zh'
  return new Intl.RelativeTimeFormat(language, { numeric: 'auto' }).format(Math.round(diffSeconds / division.divisor), division.unit)
}

const TWELVE_HOURS_MS = 12 * 3600_000

function intlLanguage(locale: string) {
  return locale.startsWith('ja') || locale.startsWith('jp') ? 'ja' : locale.startsWith('en') ? 'en' : 'zh-CN'
}

/** 完整的本地时间(悬停提示用):同一年省略年份,跨年带年份。 */
export function formatAbsoluteTime(value: string | number | undefined, locale: string, now = Date.now()): string {
  if (value === undefined || value === null || value === '') return ''
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return ''
  const sameYear = date.getFullYear() === new Date(now).getFullYear()
  return new Intl.DateTimeFormat(intlLanguage(locale), {
    ...(sameYear ? null : { year: 'numeric' }),
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date)
}

/** 消息时间:12 小时内用相对时间(3 分钟前),超过 12 小时用具体时间(11/02 21:38)。 */
export function formatMessageTime(value: string | number | undefined, locale: string, now = Date.now()): string {
  if (value === undefined || value === null || value === '') return ''
  const timestamp = new Date(value).getTime()
  if (!Number.isFinite(timestamp)) return ''
  return Math.abs(now - timestamp) < TWELVE_HOURS_MS
    ? formatRelativeTime(timestamp, locale, now)
    : formatAbsoluteTime(timestamp, locale, now)
}
