export function SourceView({ content }: { content: string }) {
  return (
    <pre className="min-w-max p-3 font-mono text-[12px] leading-5"><code>{content.split('\n').map((line, index) => (
      <span key={index} className="flex">
        <span className="w-10 shrink-0 select-none pr-3 text-right tabular-nums text-faint">{index + 1}</span>
        <span className="whitespace-pre">{line || ' '}</span>
      </span>
    ))}</code></pre>
  )
}
