import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CircleHelp, LoaderCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { QUESTION_OTHER } from '@/features/agent/api/agent'
import type { AgentQuestion, AgentQuestionAnswer } from '@/features/agent/lib/agent-events'
import { buildAnswers, emptyDraft, optionIndexForDigit, QUESTION_OTHER_MAX, shouldSubmitOnEnter, validateAnswers, validateQuestionDraft, type QuestionDraftState } from '@/features/agent/lib/question-answers'

type AgentQuestionCardProps = {
  question: AgentQuestion
  submitting?: boolean
  onSubmit: (answers: AgentQuestionAnswer[]) => void
  onSkip: () => void
}

/** 提问卡片（待回答态）：多题逐题回答后确认，单题直接提交 */
export function AgentQuestionCard({ question, submitting, onSubmit, onSkip }: AgentQuestionCardProps) {
  const { t } = useTranslation('agent')
  const [state, setState] = useState<QuestionDraftState>(() => emptyDraft(question.items))
  const [step, setStep] = useState(0)
  const [showError, setShowError] = useState(false)
  const otherTextareaRef = useRef<HTMLTextAreaElement | null>(null)

  const confirming = step === question.items.length
  const current = confirming ? undefined : question.items[step]
  const currentDraft = state[step] ?? { selected: [], other: '' }
  const currentErrorKey = current ? validateQuestionDraft(current, currentDraft) : null
  const errorKey = confirming ? validateAnswers(question.items, state) : currentErrorKey

  // 当前题选了「其他」且文本为空时，自动聚焦文本框
  useEffect(() => {
    if (!confirming && currentDraft.selected.includes(QUESTION_OTHER) && currentDraft.other === '') {
      otherTextareaRef.current?.focus?.()
    }
  }, [confirming, currentDraft.selected, currentDraft.other])

  const toggleOption = (index: number, label: string) => {
    if (submitting) return
    setShowError(false)
    setState((prev) => {
      const draft = prev[index] ?? { selected: [], other: '' }
      if (!question.items[index]!.multiSelect) {
        return { ...prev, [index]: { ...draft, selected: [label], other: label === QUESTION_OTHER ? draft.other : '' } }
      }
      const selected = draft.selected.includes(label) ? draft.selected.filter((item) => item !== label) : [...draft.selected, label]
      return { ...prev, [index]: { ...draft, selected, other: selected.includes(QUESTION_OTHER) ? draft.other : '' } }
    })
  }

  const submit = () => {
    if (submitting) return
    if (validateAnswers(question.items, state)) {
      setShowError(true)
      return
    }
    onSubmit(buildAnswers(question.items, state))
  }

  const next = () => {
    if (submitting) return
    if (currentErrorKey) {
      setShowError(true)
      return
    }
    setShowError(false)
    setStep((value) => Math.min(question.items.length, value + 1))
  }

  const goToStep = (index: number) => {
    setShowError(false)
    setStep(index)
  }

  const primaryAction = confirming || question.items.length === 1 ? submit : next

  const handleKeyDown = (event: React.KeyboardEvent) => {
    // 数字键只选择当前题的选项
    if (!confirming && /^[1-9]$/.test(event.key) && !event.metaKey && !event.ctrlKey && !event.altKey) {
      const target = event.target as HTMLElement
      if (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT') return
      const index = current ? optionIndexForDigit(event.key, current) : null
      if (index !== null && index >= 0) {
        event.preventDefault()
        toggleOption(step, current!.options[index]!.label)
      }
      return
    }
    if (shouldSubmitOnEnter({ key: event.key, inTextarea: (event.target as HTMLElement).tagName === 'TEXTAREA', metaKey: event.metaKey, ctrlKey: event.ctrlKey })) {
      event.preventDefault()
      primaryAction()
    }
  }

  return (
    <div
      role="group"
      aria-label={t('question.ariaCard')}
      onKeyDown={handleKeyDown}
      className="rounded-xl border border-tint/40 bg-tint/8 px-4 py-3"
    >
      <div className="flex items-center gap-2 text-sm font-medium text-foreground">
        <CircleHelp className="h-4 w-4 shrink-0 text-tint" />
        <span>{t('question.title')}</span>
        {!confirming && question.items.length > 1 ? (
          <span className="ml-auto shrink-0 text-xs font-normal text-faint tabular-nums">
            {t('question.progress', { current: step + 1, total: question.items.length })}
          </span>
        ) : null}
      </div>

      {!confirming && current ? (
        <div className="mt-3 space-y-2">
          <div
            role="group"
            aria-labelledby={`question-${question.id}-header-${step}`}
            className="space-y-2"
          >
            <div id={`question-${question.id}-header-${step}`} className="space-y-1">
              {current.header ? (
                <span className="inline-block max-w-full truncate rounded-full bg-fill-3 px-2 py-0.5 text-xs text-label-2">{current.header}</span>
              ) : null}
              <p className="text-sm font-medium leading-6 text-foreground">{current.question}</p>
            </div>
            <div className="space-y-1">
              {current.options.map((option, optionIndex) => (
                <QuestionOptionRow
                  key={option.label}
                  label={option.label}
                  description={option.description}
                  index={optionIndex + 1}
                  checked={currentDraft.selected.includes(option.label)}
                  multi={current.multiSelect}
                  disabled={submitting}
                  onClick={() => toggleOption(step, option.label)}
                />
              ))}
              <QuestionOptionRow
                label={t('question.otherLabel')}
                checked={currentDraft.selected.includes(QUESTION_OTHER)}
                multi={current.multiSelect}
                disabled={submitting}
                onClick={() => toggleOption(step, QUESTION_OTHER)}
              />
            </div>
            {currentDraft.selected.includes(QUESTION_OTHER) ? (
              <div className="space-y-1">
                <textarea
                  ref={currentDraft.other === '' ? otherTextareaRef : undefined}
                  value={currentDraft.other}
                  disabled={submitting}
                  onChange={(event) => {
                    setShowError(false)
                    setState((prev) => ({ ...prev, [step]: { ...(prev[step] ?? { selected: [], other: '' }), other: event.target.value.slice(0, QUESTION_OTHER_MAX) } }))
                  }}
                  placeholder={t('question.otherPlaceholder')}
                  rows={2}
                  aria-label={t('question.otherLabel')}
                  className="w-full resize-none rounded-lg border border-[hsl(var(--glass-border))] bg-[hsl(var(--surface-base))] px-2.5 py-2 text-sm leading-6 text-foreground placeholder:text-label-3 focus:outline-none"
                />
                <p className="text-right text-[11px] text-faint tabular-nums">{t('question.remaining', { count: QUESTION_OTHER_MAX - currentDraft.other.length })}</p>
              </div>
            ) : null}
          </div>
        </div>
      ) : (
        <div className="mt-3 space-y-3">
          <p className="text-sm font-medium leading-6 text-foreground">{t('question.confirmTitle')}</p>
          <div className="space-y-2">
            {question.items.map((item, index) => {
              const answer = buildAnswers(question.items, state)[index]
              const labels = (answer?.selected ?? []).filter((label) => label !== QUESTION_OTHER)
              if (answer?.selected.includes(QUESTION_OTHER)) labels.push(`${t('question.otherLabel')}：${answer.other ?? ''}`)
              return (
                <div key={index} className="flex min-w-0 items-center gap-1.5 text-sm leading-6">
                  {item.header ? <span className="shrink-0 rounded-full bg-fill-3 px-2 py-0.5 text-xs text-label-2">{item.header}</span> : null}
                  <span className="min-w-0 text-label-2">{item.question}</span>
                  <span className="shrink-0 text-faint">→</span>
                  <span className="min-w-0 text-label">{labels.join(t('question.summaryJoin'))}</span>
                  <Button size="sm" variant="ghost" className="ml-auto h-auto shrink-0 px-2 py-0.5 text-xs text-label-2" disabled={submitting} onClick={() => goToStep(index)}>
                    {t('question.edit')}
                  </Button>
                </div>
              )
            })}
          </div>
        </div>
      )}

      {showError && errorKey ? (
        <p role="alert" className="mt-2 text-xs text-red-500">{t(errorKey)}</p>
      ) : null}
      <div className="mt-3 flex flex-wrap gap-2">
        {step > 0 ? (
          <Button size="sm" variant="outline" className="shrink-0 text-sm" disabled={submitting} onClick={() => goToStep(step - 1)}>
            {t('question.back')}
          </Button>
        ) : null}
        <Button size="sm" className="shrink-0 text-sm" loading={submitting && (confirming || question.items.length === 1)} disabled={submitting} onClick={primaryAction}>
          {t(confirming || question.items.length === 1 ? 'question.submit' : 'question.next')}
        </Button>
        {!confirming ? (
          <Button size="sm" variant="outline" className="shrink-0 text-sm" disabled={submitting} onClick={onSkip}>
            {t('question.skip')}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

type QuestionOptionRowProps = {
  label: string
  description?: string
  index?: number
  checked: boolean
  multi: boolean
  disabled?: boolean
  onClick: () => void
}

/** 选项行：整行可点，radio/checkbox 语义，触控高度 ≥ 44px */
function QuestionOptionRow({ label, description, index, checked, multi, disabled, onClick }: QuestionOptionRowProps) {
  return (
    <button
      type="button"
      role={multi ? 'checkbox' : 'radio'}
      aria-checked={checked}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'flex min-h-11 w-full items-center gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors',
        checked
          ? 'border-tint/50 bg-tint/12 text-foreground'
          : 'border-[hsl(var(--glass-border))] bg-[hsl(var(--surface-base))] text-foreground hover:bg-[hsl(var(--surface-subtle))]',
      )}
    >
      <span
        aria-hidden
        className={cn(
          'grid h-4 w-4 shrink-0 place-items-center border',
          multi ? 'rounded-[4px]' : 'rounded-full',
          checked ? 'border-tint bg-tint' : 'border-label-3',
        )}
      >
        {checked ? <span className={cn('bg-[hsl(var(--surface-base))]', multi ? 'h-2 w-2 rounded-[2px]' : 'h-2 w-2 rounded-full')} /> : null}
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-sm leading-6">
          {index !== undefined && index <= 9 ? <span className="mr-1.5 text-[11px] text-faint tabular-nums">{index}</span> : null}
          {label}
        </span>
        {description ? <span className="block text-xs leading-5 text-muted-soft">{description}</span> : null}
      </span>
    </button>
  )
}

/** 已回答/跳过/取消态的引用块摘要（回复区渲染） */
export function AgentQuestionSummary({ question }: { question: AgentQuestion }) {
  const { t } = useTranslation('agent')
  return (
    <div className="border-l-2 border-[hsl(var(--glass-divider))] pl-3 text-xs leading-6">
      {question.items.map((item, index) => {
        const answer = question.answers?.[index]
        let answerText: string
        if (question.status === 'skipped') answerText = question.auto ? t('question.timedOutSkipped') : t('question.skippedLabel')
        else if (question.status === 'cancelled') answerText = t('question.cancelledLabel')
        else {
          const labels = (answer?.selected ?? []).filter((label) => label !== QUESTION_OTHER)
          if (answer?.selected.includes(QUESTION_OTHER)) labels.push(`${t('question.otherLabel')}：${answer.other ?? ''}`)
          answerText = labels.join(t('question.summaryJoin'))
        }
        return (
          <p key={index} className="flex min-w-0 items-baseline gap-1.5">
            {item.header ? <span className="shrink-0 rounded-full bg-fill-3 px-2 py-0.5 text-[11px] text-label-2">{item.header}</span> : null}
            <span className="min-w-0 text-label-2">{item.question}</span>
            <span className="shrink-0 text-faint">→</span>
            <span className="min-w-0 text-label">{answerText}</span>
          </p>
        )
      })}
    </div>
  )
}

/** 加载图标单独导出供测试占位（保持与 Button 的 loading 视觉一致） */
export function QuestionSubmittingIcon() {
  return <LoaderCircle className="h-3.5 w-3.5 animate-spin" aria-hidden />
}
