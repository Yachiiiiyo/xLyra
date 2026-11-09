const urls = new WeakMap<Blob, string>()
const registry = typeof FinalizationRegistry === 'undefined'
  ? null
  : new FinalizationRegistry<string>((url) => URL.revokeObjectURL(url))

/**
 * 同一个 Blob 始终返回同一个对象 URL,Blob 被回收时才作废。
 * 不能在组件的 effect 清理里 revoke:StrictMode 会模拟一次卸载,缓存里仍在使用的 URL 会被作废成破图。
 */
export function objectUrlFor(blob: Blob): string {
  let url = urls.get(blob)
  if (!url) {
    url = URL.createObjectURL(blob)
    urls.set(blob, url)
    registry?.register(blob, url)
  }
  return url
}
