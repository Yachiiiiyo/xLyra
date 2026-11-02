import { useRef, useState } from 'react'
import { fieldFocusClass, fieldSurfaceClass } from '@/components/ui/field-styles'
import { cn } from '@/lib/utils'

/** 默认 4+4：两组各 4 位，中间用分隔符隔开。 */
const inputOTP4x4 = [4, 4] as const

const slotChar = /[0-9a-z]/i

type InputOTPProps = {
  value?: string
  defaultValue?: string
  onValueChange?: (value: string) => void
  /** 每组位数。4+4 传 [4, 4]。 */
  groups?: readonly number[]
  separator?: string
  readOnly?: boolean
  disabled?: boolean
  id?: string
  name?: string
  autoFocus?: boolean
  'aria-label'?: string
  className?: string
  testId?: string
}

/** 分组验证码输入。value 可带分隔符，组件只保留字母和数字。 */
export function InputOTP({
  value,
  defaultValue = '',
  onValueChange,
  groups = inputOTP4x4,
  separator = '-',
  readOnly = false,
  disabled = false,
  id,
  name,
  autoFocus = false,
  'aria-label': ariaLabel,
  className,
  testId,
}: InputOTPProps) {
  const [uncontrolled, setUncontrolled] = useState(defaultValue)
  const slotsRef = useRef<Array<HTMLInputElement | null>>([])
  const current = value ?? uncontrolled
  const length = groups.reduce((sum, size) => sum + size, 0)
  const chars = otpChars(current, length)
  const formatted = formatOTPValue(chars, groups, separator)
  const locked = readOnly || disabled
  const groupStarts = groups.map((_, groupIndex) => groups.slice(0, groupIndex).reduce((sum, size) => sum + size, 0))

  function commit(nextChars: string[], cursor: number) {
    const next = formatOTPValue(nextChars, groups, separator)
    if (value === undefined) setUncontrolled(next)
    onValueChange?.(next)
    const target = Math.min(Math.max(cursor, 0), length - 1)
    slotsRef.current[target]?.focus()
  }

  function writeAt(index: number, incoming: string) {
    if (locked) return
    const { chars: next, cursor } = insertOTPChars(chars, index, incoming, length)
    commit(next, cursor)
  }

  return (
    <div role="group" aria-label={ariaLabel} className={cn('flex items-center justify-center gap-2', className)}>
      <input
        id={id}
        name={name}
        value={formatted}
        readOnly
        tabIndex={-1}
        data-testid={testId}
        aria-hidden={readOnly ? undefined : true}
        className="sr-only"
        onChange={() => {}}
      />
      {groups.map((size, groupIndex) => {
        const start = groupStarts[groupIndex]
        return (
          <div key={start} className="flex items-center gap-2">
            {groupIndex > 0 ? (
              <span aria-hidden className="text-title-3 text-label-3">{separator}</span>
            ) : null}
            <div className="flex gap-1.5">
              {Array.from({ length: size }, (_, offset) => {
                const index = start + offset
                return (
                  <input
                    key={index}
                    ref={(node) => { slotsRef.current[index] = node }}
                    value={chars[index] ?? ''}
                    readOnly={readOnly}
                    disabled={disabled}
                    inputMode="text"
                    autoCapitalize="characters"
                    autoCorrect="off"
                    autoComplete={index === 0 ? 'one-time-code' : 'off'}
                    spellCheck={false}
                    maxLength={length}
                    aria-hidden={readOnly || undefined}
                    aria-label={readOnly ? undefined : (ariaLabel ? `${ariaLabel} ${index + 1}` : undefined)}
                    data-otp-slot={String(index + 1)}
                    autoFocus={autoFocus && index === 0}
                    tabIndex={readOnly ? -1 : 0}
                    onChange={(event) => writeAt(index, event.target.value)}
                    onKeyDown={(event) => {
                      if (locked) return
                      if (event.key === 'Backspace') {
                        event.preventDefault()
                        const removeAt = chars[index] ? index : index - 1
                        if (removeAt < 0) return
                        commit(chars.filter((_, slot) => slot !== removeAt), removeAt)
                        return
                      }
                      if (event.key === 'ArrowLeft') {
                        event.preventDefault()
                        slotsRef.current[index - 1]?.focus()
                      } else if (event.key === 'ArrowRight') {
                        event.preventDefault()
                        slotsRef.current[index + 1]?.focus()
                      }
                    }}
                    onPaste={(event) => {
                      if (locked) return
                      event.preventDefault()
                      writeAt(index, event.clipboardData.getData('text'))
                    }}
                    className={cn(
                      fieldSurfaceClass,
                      fieldFocusClass,
                      'h-11 w-9 text-center font-mono text-title-3 uppercase caret-transparent sm:w-11',
                      readOnly && 'cursor-default',
                    )}
                  />
                )
              })}
            </div>
          </div>
        )
      })}
    </div>
  )
}

// 测试要直接断言这几个纯函数；组件文件里多导出它们会触发 react-refresh。
// eslint-disable-next-line react-refresh/only-export-components
export function otpChars(value: string, length: number) {
  return value.replace(/[^0-9a-z]/gi, '').toUpperCase().slice(0, length).split('')
}

// eslint-disable-next-line react-refresh/only-export-components
export function formatOTPValue(chars: readonly string[], groups: readonly number[], separator: string) {
  const parts: string[] = []
  let index = 0
  for (const size of groups) {
    const part = chars.slice(index, index + size).filter(Boolean).join('')
    if (!part) break
    parts.push(part)
    index += size
    if (part.length < size) break
  }
  return parts.join(separator)
}

// eslint-disable-next-line react-refresh/only-export-components
export function insertOTPChars(chars: readonly string[], index: number, incoming: string, length: number) {
  const next = chars.slice()
  while (next.length < length) next.push('')
  const clean = incoming.replace(/[^0-9a-z]/gi, '').toUpperCase()
  let cursor = index
  for (const char of clean) {
    if (!slotChar.test(char) || cursor >= length) break
    next[cursor] = char
    cursor += 1
  }
  return { chars: next.slice(0, length), cursor }
}
