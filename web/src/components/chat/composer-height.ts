/** textarea 的行高（text-[15px] leading-6 = 24px）与自动增高的高度上限 */
export const COMPOSER_LINE_HEIGHT = 24
export const COMPOSER_MAX_HEIGHT = 220

/** 自动增高后的 textarea 高度：不低于 minRows 行的下限，不超过上限 */
export function composerTextareaHeight(scrollHeight: number, minRows = 1): number {
  const minHeight = Math.max(minRows, 1) * COMPOSER_LINE_HEIGHT
  return Math.min(Math.max(scrollHeight, minHeight), COMPOSER_MAX_HEIGHT)
}
