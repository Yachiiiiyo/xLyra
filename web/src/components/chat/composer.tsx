import { useLayoutEffect, useRef, type ReactNode } from 'react'
import { ArrowUp, Square } from 'lucide-react'
import { cn } from '@/lib/utils'
import { ClawdPet } from '@/components/chat/clawd-pet'
import { composerTextareaHeight } from '@/components/chat/composer-height'
import { filesFromClipboard } from '@/lib/clipboard-files'

type ComposerProps = {
  value: string
  onChange: (value: string) => void
  onSubmit: () => void
  onStop?: () => void
  streaming?: boolean
  canSubmit: boolean
  placeholder: string
  controls?: ReactNode
  trailingControls?: ReactNode
  attachments?: ReactNode
  onPasteFiles?: (files: File[]) => boolean
  ghostText?: string
  onAcceptGhost?: () => void
  /** 灰字右侧 Tab 提示按钮的无障碍文案 */
  ghostAcceptLabel?: string
  stopButtonClassName?: string
  stopIconClassName?: string
  sendButtonTitle?: string
  /** 输入框最少展示的行数，自动增高不会低于这个下限（默认 1 行） */
  minRows?: number
  /** 输入框右上角趴一只会动的小螃蟹，默认开启 */
  mascot?: boolean
}

export function Composer({
  value,
  onChange,
  onSubmit,
  onStop,
  streaming = false,
  canSubmit,
  placeholder,
  controls,
  trailingControls,
  attachments,
  onPasteFiles,
  ghostText,
  onAcceptGhost,
  ghostAcceptLabel,
  stopButtonClassName,
  stopIconClassName,
  sendButtonTitle,
  minRows = 1,
  mascot = true,
}: ComposerProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const showGhost = value === '' && Boolean(ghostText)

  useLayoutEffect(() => {
    const el = textareaRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${composerTextareaHeight(el.scrollHeight, minRows)}px`
  }, [value, minRows])

  const handleKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    const noModifiers = !event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey
    const atEnd = event.currentTarget.selectionStart === value.length && event.currentTarget.selectionEnd === value.length
    if (showGhost && onAcceptGhost && noModifiers && !event.nativeEvent.isComposing
      && (event.key === 'Tab' || (event.key === 'ArrowRight' && atEnd))) {
      event.preventDefault()
      onAcceptGhost()
      return
    }
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault()
      if (canSubmit) onSubmit()
    }
  }

  const handlePaste = (event: React.ClipboardEvent<HTMLTextAreaElement>) => {
    if (!onPasteFiles) return
    const pastedFiles = filesFromClipboard(event.clipboardData)
    if (pastedFiles.length > 0 && onPasteFiles(pastedFiles)) event.preventDefault()
  }

  return (
    <div className="relative">
      {mascot ? <ClawdPet value={value} streaming={streaming} /> : null}
      <div
        data-chat-composer="true"
        // 输入框：浮起的面板质感，见 index.css .composer-surface
        className="composer-surface rounded-[26px] px-3 pb-2 pt-3"
      >
        {attachments ? <div className="px-1 pb-2">{attachments}</div> : null}
        <div className="relative">
          <textarea
            ref={textareaRef}
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
            placeholder={showGhost ? '' : placeholder}
            rows={minRows}
            className="block max-h-[220px] w-full resize-none bg-transparent px-1.5 text-[15px] leading-6 text-label outline-none placeholder:text-label-3"
          />
          {showGhost ? (
            <div className="pointer-events-none absolute inset-0 overflow-hidden whitespace-pre-wrap break-words px-1.5 text-[15px] leading-6 text-label-3">
              <span aria-hidden="true">{ghostText}</span>
              <button
                type="button"
                aria-label={ghostAcceptLabel}
                title={ghostAcceptLabel}
                onClick={() => onAcceptGhost?.()}
                className="pointer-events-auto ml-1.5 mt-0.5 inline-flex h-5 items-center rounded border border-separator bg-fill-2 px-1.5 align-top text-[10px] leading-4 text-label-3 transition-colors hover:text-label-2"
              >
                <kbd>Tab</kbd>
              </button>
            </div>
          ) : null}
        </div>
        <div className="mt-1 flex items-center justify-between gap-2">
          <div className="flex shrink-0 items-center gap-1">{controls}</div>
          <div className="flex min-w-0 flex-1 items-center justify-end gap-1">
            {trailingControls}
            {streaming ? (
              <button
                type="button"
                onClick={onStop}
                className={cn('flex h-11 w-11 shrink-0 items-center justify-center rounded-full bg-label text-canvas transition-opacity hover:opacity-90 md:h-9 md:w-9', stopButtonClassName)}
                aria-label="stop"
              >
                <Square className={cn('h-3.5 w-3.5 fill-current', stopIconClassName)} />
              </button>
            ) : (
              <button
                type="button"
                onClick={onSubmit}
                disabled={!canSubmit}
                className={cn(
                  'flex h-11 w-11 shrink-0 items-center justify-center rounded-full transition-opacity md:h-9 md:w-9',
                  canSubmit
                    ? 'bg-tint text-tint-foreground hover:opacity-90'
                    : 'cursor-not-allowed bg-fill-2 text-label-3',
                )}
                aria-label="send"
                title={sendButtonTitle}
              >
                <ArrowUp className="h-4 w-4" />
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
