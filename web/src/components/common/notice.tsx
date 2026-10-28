import { AlertTriangle } from 'lucide-react'

export function Notice({ items, tone = 'default' }: { items: string[]; tone?: 'default' | 'danger' | 'warning' }) {
  const iconTone = tone === 'danger' ? 'text-danger' : tone === 'warning' ? 'text-warning' : 'text-tint'
  return (
    <div className="rounded-inner bg-fill-4 px-4 py-3">
      <div className="flex gap-3">
        <AlertTriangle className={`mt-0.5 h-4 w-4 shrink-0 ${iconTone}`} />
        <ul className="space-y-1 text-footnote leading-5 text-label-2">
          {items.map((item) => <li key={item}>{item}</li>)}
        </ul>
      </div>
    </div>
  )
}
