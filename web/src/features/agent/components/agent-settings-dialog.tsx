import { useCallback, useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ChevronRight, Layers, LoaderCircle, MonitorCog, MoveLeft, Pencil, Plus, Search, Settings2, Sparkles, X } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { AgentConfirmDialog } from '@/features/agent/components/agent-confirm-dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { TextArea } from '@/components/ui/textarea'
import { Slider } from '@/components/ui/slider'
import {
  deleteAgentSkill,
  fetchAgentCapabilities,
  fetchAgentConfigEnvelope,
  fetchAgentSkillDetail,
  fetchAgentSkillFile,
  listAgentSkills,
  updateAgentSkill,
  updateAgentCapabilities,
  updateAgentContextSettings,
  type AgentSkill,
} from '@/features/agent/api/agent'
import { buildSkillTemplate, isValidSkillName, syncSkillNameInContent } from '@/features/agent/lib/skill-editor'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'
import { useMobileLayout } from '@/hooks/use-media-query'
import { AgentScopePane } from '@/features/agent/components/agent-scope-pane'
import { AgentEnvironmentPane } from '@/features/agent/components/agent-environment-pane'

const capabilitiesKey = ['agent', 'capabilities'] as const
const skillsKey = ['agent', 'skills'] as const

type SettingsTab = 'scope' | 'skills' | 'context' | 'environment'
type MobileSettingsView = 'menu' | 'detail'
type SettingsActions = {
  save: () => void
  canSave: boolean
  pending: boolean
  /** 当前 tab 有未保存的草稿时为 true：底部出现「未保存的更改」提示 */
  dirty?: boolean
}

/** In-dialog navigation for Skills: list / detail / edit (name null means creating). */
type SkillsNav =
  | { view: 'list' }
  | { view: 'detail'; name: string }
  | { view: 'edit'; name: string | null }

type AgentSettingsDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** Agent 设置弹窗：主程序 Dialog 外壳（桌面居中 / 移动端底部抽屉），左侧纵向导航 + 内容 + 底部操作栏。 */
export function AgentSettingsDialog({ open, onOpenChange }: AgentSettingsDialogProps) {
  const { t } = useTranslation('agent')
  const mobileLayout = useMobileLayout()
  const [tab, setTab] = useState<SettingsTab>('scope')
  const [mobileView, setMobileView] = useState<MobileSettingsView>('menu')
  const [paneActions, setPaneActions] = useState<SettingsActions | null>(null)
  const [skillsNav, setSkillsNav] = useState<SkillsNav>({ view: 'list' })

  function resetDialogState() {
    setTab('scope')
    setMobileView('menu')
    setSkillsNav({ view: 'list' })
    setPaneActions(null)
  }

  function closeDialog() {
    resetDialogState()
    onOpenChange(false)
  }

  const tabs: Array<{ key: SettingsTab; label: string; icon: typeof Sparkles }> = [
    { key: 'scope', label: t('settings.tabScope'), icon: Settings2 },
    { key: 'skills', label: t('settings.tabSkills'), icon: Sparkles },
    { key: 'context', label: t('settings.tabContext'), icon: Layers },
    { key: 'environment', label: t('settings.tabEnvironment'), icon: MonitorCog },
  ]

  // Header back button: detail/edit views go one level up (editing an existing skill returns to detail, otherwise to the list).
  const headerBack = tab === 'skills' && skillsNav.view !== 'list'
    ? () => {
        if (skillsNav.view === 'edit' && skillsNav.name) setSkillsNav({ view: 'detail', name: skillsNav.name })
        else setSkillsNav({ view: 'list' })
      }
    : null

  const tabContent = tab === 'scope'
    ? <AgentScopePane onActionsChange={setPaneActions} />
    : tab === 'skills'
      ? <SkillsPane nav={skillsNav} onNavigate={setSkillsNav} onActionsChange={setPaneActions} />
      : tab === 'context'
        ? <ContextPane onActionsChange={setPaneActions} />
        : <AgentEnvironmentPane />

  const actionsFooter = (
    paneActions ? (
      <Button onClick={paneActions.save} disabled={!paneActions.canSave || paneActions.pending}>{t('settings.save')}</Button>
    ) : null
  )
  // 编辑器类 tab（skill 编辑）自带内部滚动，外层改为不滚动防双层滚动条
  const editorTab = tab === 'skills' && skillsNav.view === 'edit'

  const dialogBody = (
    <div className="flex min-h-0 flex-1">
      <aside className="flex w-48 shrink-0 flex-col gap-0.5 border-r border-[hsl(var(--glass-divider))] p-3">
        <p className="px-2.5 pb-2 pt-1.5 text-[11px] font-semibold uppercase tracking-[0.18em] text-faint">
          {t('settings.title')}
        </p>
        {tabs.map((item) => (
          <button
            key={item.key}
            type="button"
            onClick={() => {
              setPaneActions(null)
              setTab(item.key)
            }}
            className={cn(
              'flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm transition-colors',
              tab === item.key
                ? 'nav-item-selected font-medium text-foreground'
                : 'text-muted-soft hover:bg-[hsl(var(--surface-subtle))] hover:text-foreground',
            )}
          >
            <item.icon className="h-4 w-4 shrink-0" />
            {item.label}
          </button>
        ))}
      </aside>
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <div className="flex shrink-0 items-center justify-between border-b border-[hsl(var(--glass-divider))] py-3.5 pl-6 pr-4">
          {headerBack ? (
            <button
              type="button"
              onClick={headerBack}
              className="flex items-center gap-1.5 text-sm font-medium text-muted-soft transition-colors hover:text-foreground"
            >
              <MoveLeft className="h-4 w-4" />
              {tabs.find((item) => item.key === tab)?.label}
            </button>
          ) : (
            <p className="text-[15px] font-semibold text-foreground">{tabs.find((item) => item.key === tab)?.label}</p>
          )}
          <button
            type="button"
            onClick={closeDialog}
            className="flex h-8 w-8 items-center justify-center rounded-lg text-muted-soft transition-colors hover:bg-[hsl(var(--surface-subtle))] hover:text-foreground"
            aria-label={t('settings.close')}
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className={cn(
          'min-h-0 flex-1 overscroll-contain px-6 py-5',
          editorTab ? 'overflow-hidden' : 'overflow-y-auto',
        )}>
          {tabContent}
        </div>
        <div className="shrink-0">
          {paneActions?.dirty ? (
            <p className="px-6 pt-3 text-xs text-amber-500">{t('settings.scope.unsavedChanges')}</p>
          ) : null}
          <DialogFooter
            className={cn('py-3', paneActions?.dirty && 'border-t-0')}
            cancel={<Button variant="outline" onClick={closeDialog} disabled={paneActions?.pending}>{t('settings.cancel')}</Button>}
            confirm={actionsFooter}
          />
        </div>
      </div>
    </div>
  )

  const mobileDialogBody = mobileView === 'menu' ? (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* 主程序的 DialogHeader 在移动端为左上角的圆形关闭/返回按钮留了位置 */}
      <DialogHeader>
        <DialogTitle>{t('settings.title')}</DialogTitle>
      </DialogHeader>
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-3 py-2">
        {tabs.map((item) => (
          <button
            key={item.key}
            type="button"
            onClick={() => {
              setPaneActions(null)
              setTab(item.key)
              setMobileView('detail')
            }}
            className="flex w-full min-h-11 items-center gap-3 border-b border-[hsl(var(--glass-divider))] px-2 py-3 text-left text-sm text-foreground last:border-b-0"
          >
            <item.icon className="h-4 w-4 shrink-0 text-muted-soft" />
            <span className="min-w-0 flex-1 truncate">{item.label}</span>
            <ChevronRight className="h-4 w-4 shrink-0 text-muted-soft" />
          </button>
        ))}
      </div>
    </div>
  ) : (
    <div className="flex min-h-0 flex-1 flex-col">
      <DialogHeader>
        <DialogTitle className="truncate">{tabs.find((item) => item.key === tab)?.label}</DialogTitle>
      </DialogHeader>
      <div className={cn(
        'min-h-0 flex-1 overscroll-contain px-4 py-4',
        editorTab ? 'overflow-hidden' : 'overflow-y-auto',
      )}>
        {tabContent}
      </div>
      {paneActions ? (
        <div className="shrink-0">
          {paneActions.dirty ? (
            <p className="px-6 pt-3 text-xs text-amber-500">{t('settings.scope.unsavedChanges')}</p>
          ) : null}
          <DialogFooter
            className={paneActions.dirty ? 'border-t-0' : undefined}
            cancel={<Button variant="outline" onClick={closeDialog} disabled={paneActions.pending}>{t('settings.cancel')}</Button>}
            confirm={actionsFooter}
          />
        </div>
      ) : null}
    </div>
  )

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => {
      if (nextOpen && mobileLayout) setMobileView('menu')
      if (!nextOpen) resetDialogState()
      onOpenChange(nextOpen)
    }}>
      <DialogContent
        size="form"
        aria-describedby={undefined}
        onBack={mobileLayout && mobileView === 'detail'
          ? () => {
              // Skills 的 list/detail/edit 先在内部分层返回，其余 tab 直接回菜单
              if (headerBack) {
                headerBack()
                return
              }
              setPaneActions(null)
              setMobileView('menu')
            }
          : undefined}
        backLabel={tabs.find((item) => item.key === tab)?.label}
        className={cn(
          mobileLayout
            ? mobileView === 'menu'
              ? 'max-h-[min(60svh,480px)]'
              : 'h-[min(78svh,680px)]'
            : 'h-[min(80svh,620px)] w-[min(94vw,920px)]',
        )}
      >
        {!mobileLayout ? <DialogTitle className="sr-only">{t('settings.title')}</DialogTitle> : null}
        {mobileLayout ? mobileDialogBody : dialogBody}
      </DialogContent>
    </Dialog>
  )
}

/** The global toggle and the enable/disable list both go through agent /config whole-save (credential fields are preserved server-side). */
function useCapabilitiesMutation() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: updateAgentCapabilities,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: capabilitiesKey })
      await queryClient.invalidateQueries({ queryKey: skillsKey })
    },
  })
}

/** Toggles a skill by maintaining the disabled_skills list in the config. */
function useSkillToggle() {
  const { t } = useTranslation('agent')
  const capabilitiesQuery = useQuery({ queryKey: capabilitiesKey, queryFn: fetchAgentCapabilities, retry: false })
  const capabilitiesMutation = useCapabilitiesMutation()
  const disabledSkills = useMemo(
    () => new Set(capabilitiesQuery.data?.disabled_skills ?? []),
    [capabilitiesQuery.data],
  )
  const toggleSkill = (skill: Pick<AgentSkill, 'name' | 'enabled'>) => {
    const next = new Set(disabledSkills)
    if (skill.enabled) next.add(skill.name)
    else next.delete(skill.name)
    capabilitiesMutation.mutate(
      { disabled_skills: [...next] },
      { onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }) },
    )
  }
  return { toggleSkill, pending: capabilitiesMutation.isPending }
}

function SkillsPane({ nav, onNavigate, onActionsChange }: {
  nav: SkillsNav
  onNavigate: (nav: SkillsNav) => void
  onActionsChange: (actions: SettingsActions | null) => void
}) {
  const { t } = useTranslation('agent')
  const skillsQuery = useQuery({ queryKey: skillsKey, queryFn: listAgentSkills, retry: false })
  const capabilitiesQuery = useQuery({ queryKey: capabilitiesKey, queryFn: fetchAgentCapabilities, retry: false })
  const capabilitiesMutation = useCapabilitiesMutation()
  const { toggleSkill, pending: togglePending } = useSkillToggle()

  const [keyword, setKeyword] = useState('')

  const globalEnabled = skillsQuery.data?.enabled ?? capabilitiesQuery.data?.enable_skills !== false
  const skills = skillsQuery.data?.skills ?? []
  const filtered = skills.filter((skill) =>
    `${skill.name} ${skill.description}`.toLowerCase().includes(keyword.trim().toLowerCase()))

  if (nav.view === 'edit') {
    return (
      <SkillEditor
        name={nav.name}
        existingNames={skills.map((skill) => skill.name)}
        onBack={() => onNavigate(nav.name ? { view: 'detail', name: nav.name } : { view: 'list' })}
        onSaved={(name) => onNavigate({ view: 'detail', name })}
        onActionsChange={onActionsChange}
      />
    )
  }

  if (nav.view === 'detail') {
    return (
      <SkillDetail
        name={nav.name}
        fallback={skills.find((skill) => skill.name === nav.name)}
        onEdit={(name) => onNavigate({ view: 'edit', name })}
        onToggle={toggleSkill}
        togglePending={togglePending}
      />
    )
  }

  return (
    <div className="space-y-4">
      <Switch
        label={t('settings.skillsEnable')}
        description={t('settings.skillsEnableHint')}
        checked={globalEnabled}
        disabled={capabilitiesQuery.data === null || capabilitiesMutation.isPending}
        onCheckedChange={(checked) => capabilitiesMutation.mutate(
          { enable_skills: checked },
          { onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }) },
        )}
      />

      {skillsQuery.data === null ? (
        <p className="rounded-lg border border-[hsl(var(--glass-border))] px-4 py-8 text-center text-xs text-muted-soft">
          {t('settings.skillsUnavailable')}
        </p>
      ) : (
        <>
          <div className="flex items-center gap-2">
            <div className="relative flex-1">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-foreground/40" />
              <Input
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                placeholder={t('settings.skillsSearch')}
                className="pl-9"
              />
            </div>
            <Button size="sm" variant="secondary" onClick={() => onNavigate({ view: 'edit', name: null })}>
              <Plus className="h-3.5 w-3.5" />
              {t('settings.skillAdd')}
            </Button>
          </div>

          <div className="overflow-hidden rounded-xl border border-[hsl(var(--glass-border))]">
            {filtered.length === 0 ? (
              <p className="px-4 py-8 text-center text-xs text-muted-soft">{t('settings.skillsEmpty')}</p>
            ) : (
              filtered.map((skill) => {
                const external = skill.ownership === 'external' || skill.editable !== true
                return (
                  <div
                    key={`${skill.scope ?? 'project'}:${skill.name}`}
                    className="flex cursor-pointer items-center gap-3 border-t border-[hsl(var(--glass-divider))] px-4 py-3 transition-colors first:border-t-0 hover:bg-[hsl(var(--surface-subtle))]"
                    onClick={() => onNavigate({ view: 'detail', name: skill.name })}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate text-sm font-medium text-foreground">{skill.name}</span>
                        {external ? <Badge variant="neutral">{t('settings.skillScopeExternal')}</Badge> : null}
                      </div>
                      <p className="mt-0.5 truncate text-xs text-muted-soft" title={skill.description}>{skill.description}</p>
                    </div>
                    <div onClick={(event) => event.stopPropagation()}>
                      <Switch
                        checked={skill.enabled}
                        disabled={togglePending}
                        onCheckedChange={() => toggleSkill(skill)}
                      />
                    </div>
                  </div>
                )
              })
            )}
          </div>
          <p className="text-xs text-faint">{t('settings.effectiveHint')}</p>
        </>
      )}
    </div>
  )
}

/** Skill detail: header (name/toggle/edit) + Overview / Contents tabs; Contents defaults to SKILL.md. */
function SkillDetail({
  name,
  fallback,
  onEdit,
  onToggle,
  togglePending,
}: {
  name: string
  fallback?: AgentSkill
  onEdit: (name: string) => void
  onToggle: (skill: Pick<AgentSkill, 'name' | 'enabled'>) => void
  togglePending: boolean
}) {
  const { t } = useTranslation('agent')
  const detailQuery = useQuery({
    queryKey: ['agent', 'skill-detail', name],
    queryFn: () => fetchAgentSkillDetail(name),
    retry: false,
  })
  const detail = detailQuery.data
  const scope = detail?.scope ?? fallback?.scope
  const editable = detail?.editable === true || fallback?.editable === true
  const enabled = detail?.enabled ?? fallback?.enabled ?? true
  const resources = useMemo(() => detail?.resources ?? [], [detail])
  const files = useMemo(() => ['SKILL.md', ...resources], [resources])

  const [selectedFile, setSelectedFile] = useState('SKILL.md')
  const fileQuery = useQuery({
    queryKey: ['agent', 'skill-file', name, selectedFile],
    queryFn: () => fetchAgentSkillFile(name, selectedFile),
    enabled: selectedFile !== 'SKILL.md',
    retry: false,
  })
  const viewingContent = selectedFile === 'SKILL.md' ? detail?.content : fileQuery.data?.content
  const viewingTruncated = selectedFile === 'SKILL.md' ? detail?.contentTruncated : fileQuery.data?.truncated

  const scopeLabel = scope === 'managed'
    ? t('settings.detailScopeManaged')
    : scope === 'user'
      ? t('settings.detailScopeUser')
      : scope === 'extra'
        ? t('settings.detailScopeExtra')
        : t('settings.detailScopeProject')
  const external = detail?.ownership === 'external' || fallback?.ownership === 'external' || !editable

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between gap-3">
        <p className="flex min-w-0 items-center gap-2 text-base font-semibold text-foreground">
          <span className="truncate">{name}</span>
          {external ? <Badge variant="neutral">{t('settings.skillScopeExternal')}</Badge> : null}
        </p>
        <div className="flex shrink-0 items-center gap-2">
          {editable ? (
            <Button size="sm" variant="outline" onClick={() => onEdit(name)}>
              <Pencil className="h-3.5 w-3.5" />
              {t('settings.detailEdit')}
            </Button>
          ) : null}
          <Switch
            checked={enabled}
            disabled={togglePending}
            onCheckedChange={() => onToggle({ name, enabled })}
          />
        </div>
      </div>

      {detailQuery.data === null && !detailQuery.isLoading ? (
        <p className="mt-6 rounded-lg border border-[hsl(var(--glass-border))] px-4 py-8 text-center text-xs text-muted-soft">
          {t('settings.detailUnavailable')}
        </p>
      ) : (
        <Tabs defaultValue="contents" className="mt-4 flex min-h-0 flex-1 flex-col">
          <TabsList className="h-9 w-60 shrink-0">
            <TabsTrigger value="overview">{t('settings.detailOverview')}</TabsTrigger>
            <TabsTrigger value="contents">{t('settings.detailContents', { count: files.length })}</TabsTrigger>
          </TabsList>

          <TabsContent value="overview">
            <p className="mt-4 whitespace-pre-wrap text-sm leading-6 text-foreground/90">
              {detail?.description ?? fallback?.description}
            </p>
            <div className="mt-4 rounded-xl border border-[hsl(var(--glass-border))]">
              <DetailRow label={t('settings.skillName')} value={name} mono />
              <DetailRow label={t('settings.detailScope')} value={scopeLabel} />
              <DetailRow label={t('settings.detailOwnership')} value={external ? t('settings.skillScopeExternal') : t('settings.detailScopeManaged')} />
              <DetailRow label={t('settings.detailPath')} value={detail?.path ?? '—'} mono />
              {detail?.license ? <DetailRow label={t('settings.detailLicense')} value={detail.license} /> : null}
            </div>
          </TabsContent>
          <TabsContent value="contents" className="flex min-h-0 flex-1 flex-col data-[state=inactive]:hidden">
            <div className="mt-4 flex shrink-0 items-center gap-3">
              <Select value={selectedFile} onValueChange={setSelectedFile}>
                <SelectTrigger className="h-8 w-56 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {files.map((file) => (
                    <SelectItem key={file} value={file} className="font-mono text-xs">{file}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <span className="text-xs text-faint">{t('settings.detailFiles', { count: files.length })}</span>
            </div>
            <div className="mt-3 min-h-0 flex-1 overflow-auto border-t border-[hsl(var(--glass-divider))]">
              {viewingContent === undefined || viewingContent === null ? (
                <p className="flex items-center gap-2 py-6 text-xs text-muted-soft">
                  <LoaderCircle className="h-3.5 w-3.5 animate-spin" />
                  {t('settings.detailLoading')}
                </p>
              ) : (
                <pre className="whitespace-pre-wrap break-all py-3 pr-2 font-mono text-xs leading-5 text-foreground/90">{viewingContent}</pre>
              )}
              {viewingTruncated ? (
                <p className="py-2 text-xs text-amber-500">{t('settings.detailTruncated')}</p>
              ) : null}
            </div>
          </TabsContent>
        </Tabs>
      )}
    </div>
  )
}

function DetailRow({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-4 border-t border-[hsl(var(--glass-divider))] px-4 py-2.5 first:border-t-0">
      <span className="shrink-0 text-xs text-muted-soft">{label}</span>
      <span className={cn('min-w-0 truncate text-xs text-foreground', mono && 'font-mono')} title={value}>{value}</span>
    </div>
  )
}

/**
 * Skill editor: edits the complete agent-owned SKILL.md.
 * name 为 null 时是新建模式：名称输入实时校验（与后端 validSkillName 一致，重名禁止），
 * 模板预填并与正文第一个 frontmatter 的 name: 行联动（手动改过该行则断开）。
 */
export function SkillEditor({ name, existingNames, onBack, onSaved, onActionsChange }: {
  name: string | null
  existingNames: string[]
  onBack: () => void
  onSaved: (name: string) => void
  onActionsChange: (actions: SettingsActions | null) => void
}) {
  const { t } = useTranslation('agent')
  const mobileLayout = useMobileLayout()
  const queryClient = useQueryClient()
  const [createdName, setCreatedName] = useState<string | null>(null)
  const editingName = createdName ?? name
  const creating = editingName === null
  const [template] = useState(() => buildSkillTemplate('', t('settings.skillTemplateDescription'), t('settings.skillBodyPlaceholder')))
  const [nameDraft, setNameDraft] = useState('')
  const [contentDraft, setContentDraft] = useState<string | null>(creating ? template : null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const bodyQuery = useQuery({
    queryKey: ['agent', 'skill-detail', editingName ?? ''],
    queryFn: () => fetchAgentSkillDetail(editingName as string),
    enabled: editingName !== null,
    retry: false,
  })

  const content = creating ? (contentDraft ?? '') : (contentDraft ?? bodyQuery.data?.content ?? '')
  const nameError = !creating || nameDraft === ''
    ? null
    : !isValidSkillName(nameDraft)
      ? t('settings.skillNameInvalid')
      : existingNames.includes(nameDraft)
        ? t('settings.skillNameTaken')
        : null
  const canSave = creating
    ? isValidSkillName(nameDraft) && !existingNames.includes(nameDraft)
    : Boolean(bodyQuery.data?.editable && !bodyQuery.isLoading && contentDraft !== null)

  const saveTarget = creating ? nameDraft : editingName
  const save = useMutation({
    mutationFn: () => updateAgentSkill(saveTarget, content),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: skillsKey })
      toast.success(t('settings.skillSaved'))
      if (creating) {
        if (mobileLayout) {
          setCreatedName(nameDraft)
          setContentDraft(null)
        } else {
          onSaved(nameDraft)
        }
        return
      }
      await queryClient.invalidateQueries({ queryKey: ['agent', 'skill-detail', editingName] })
      if (mobileLayout) setContentDraft(null)
      else onBack()
    },
    onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }),
  })

  const { mutate: saveMutate, isPending: savePending } = save
  const saveAction = useCallback(() => saveMutate(), [saveMutate])
  const dirty = creating ? contentDraft !== template : contentDraft !== null

  useEffect(() => {
    onActionsChange({ save: saveAction, canSave, pending: savePending, dirty })
    return () => onActionsChange(null)
  }, [canSave, dirty, onActionsChange, saveAction, savePending])

  function handleNameChange(next: string) {
    setContentDraft((current) => syncSkillNameInContent(current ?? '', nameDraft, next))
    setNameDraft(next)
  }

  const remove = useMutation({
    mutationFn: () => deleteAgentSkill(editingName as string),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: skillsKey })
      queryClient.removeQueries({ queryKey: ['agent', 'skill-detail', editingName] })
      toast.success(t('settings.skillDeleted'))
      onBack()
    },
    onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }),
  })

  return (
    <div className="flex h-full flex-col space-y-4">
      <p className="text-sm font-semibold text-foreground">{creating ? t('settings.skillCreate') : editingName}</p>

      {creating ? (
        <label className="block space-y-1.5">
          <span className="text-xs font-medium text-muted-soft">{t('settings.skillName')}</span>
          <Input
            value={nameDraft}
            onChange={(event) => handleNameChange(event.target.value)}
            placeholder="my-skill"
            inputMode="text"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            className="h-9 font-mono text-sm"
            aria-label={t('settings.skillName')}
          />
          {nameError ? <span className="block text-xs text-red-500">{nameError}</span> : null}
        </label>
      ) : null}

      <label className="flex min-h-0 flex-1 flex-col space-y-1.5">
        <span className="text-xs font-medium text-muted-soft">{t('settings.skillBody')}</span>
        <TextArea
          value={content}
          onChange={(event) => setContentDraft(event.target.value)}
          placeholder={t('settings.skillBodyPlaceholder')}
          className="h-0 min-h-0 flex-1 resize-none font-mono text-xs leading-5 focus:ring-inset"
          disabled={!creating && (bodyQuery.isLoading || bodyQuery.data?.editable !== true)}
        />
      </label>

      <div className="flex items-center gap-2">
        {!creating && bodyQuery.data?.deletable === true ? (
          <Button variant="ghost" className="text-destructive" onClick={() => setConfirmDelete(true)}>
            {t('settings.skillDelete')}
          </Button>
        ) : null}
      </div>

      <AgentConfirmDialog
        open={confirmDelete}
        title={t('settings.skillDelete')}
        description={t('settings.skillDeleteConfirm')}
        confirmLabel={t('settings.skillDelete')}
        destructive
        onCancel={() => setConfirmDelete(false)}
        onConfirm={() => {
          setConfirmDelete(false)
          remove.mutate()
        }}
      />
    </div>
  )
}

const COMPACT_RATIO_MIN = 50
const COMPACT_RATIO_MAX = 95
const COMPACT_RATIO_DEFAULT = 90

/** 上下文设置：自动压缩水位线（全局）+ 全局上下文窗口回退。 */
function ContextPane({ onActionsChange }: { onActionsChange: (actions: SettingsActions | null) => void }) {
  const { t } = useTranslation('agent')
  const queryClient = useQueryClient()
  const envelopeQuery = useQuery({ queryKey: ['agent', 'config-envelope'], queryFn: fetchAgentConfigEnvelope, retry: false })

  const [ratioDraft, setRatioDraft] = useState<number | null>(null)
  const [windowDraft, setWindowDraft] = useState<string | null>(null)

  const savedRatio = envelopeQuery.data?.agent?.compact_trigger_ratio
  const ratio = ratioDraft ?? Math.round((typeof savedRatio === 'number' ? savedRatio : 0.9) * 100)

  const savedWindow = envelopeQuery.data?.agent?.context_window
  const windowValue = windowDraft ?? (typeof savedWindow === 'number' ? String(savedWindow) : '')
  const windowParsed = Number(windowValue.trim())
  const windowValid = windowValue.trim() === '' || (Number.isFinite(windowParsed) && windowParsed > 0)

  const save = useMutation({
    mutationFn: () => updateAgentContextSettings({
      compactTriggerRatio: ratio / 100,
      contextWindow: windowValue.trim() === '' ? undefined : windowParsed,
    }),
    onSuccess: async () => {
      setRatioDraft(null)
      setWindowDraft(null)
      await queryClient.invalidateQueries({ queryKey: ['agent', 'config-envelope'] })
      await queryClient.invalidateQueries({ queryKey: capabilitiesKey })
      toast.success(t('settings.context.saved'))
    },
    onError: (error) => toast.error(t('settings.saveFailed'), { description: error.message }),
  })

  const { mutate: saveMutate, isPending: savePending } = save
  const saveAction = useCallback(() => saveMutate(), [saveMutate])
  const dirty = ratioDraft !== null || windowDraft !== null

  useEffect(() => {
    onActionsChange({ save: saveAction, canSave: windowValid && !envelopeQuery.isLoading && envelopeQuery.data !== null, pending: savePending, dirty })
    return () => onActionsChange(null)
  }, [dirty, windowValid, envelopeQuery.isLoading, envelopeQuery.data, onActionsChange, saveAction, savePending])

  if (envelopeQuery.data === null && !envelopeQuery.isLoading) {
    return (
      <p className="rounded-lg border border-[hsl(var(--glass-border))] px-4 py-8 text-center text-xs text-muted-soft">
        {t('settings.context.unavailable')}
      </p>
    )
  }

  return (
    <div className="space-y-6">
      <section className="space-y-3">
        <div>
          <h3 className="text-sm font-semibold text-foreground">{t('settings.context.windowTitle')}</h3>
          <p className="mt-1 text-xs leading-5 text-muted-soft">{t('settings.context.windowHint')}</p>
        </div>
        <label className="block space-y-1.5">
          <span className="text-xs font-medium text-muted-soft">{t('settings.context.windowLabel')}</span>
          <Input
            value={windowValue}
            onChange={(event) => setWindowDraft(event.target.value.replace(/[^0-9]/g, ''))}
            placeholder={t('settings.context.windowPlaceholder')}
            inputMode="numeric"
            className="h-9 max-w-52 text-right font-mono text-sm tabular-nums"
            aria-label={t('settings.context.windowLabel')}
          />
          {!windowValid ? (
            <span className="text-xs text-red-500">{t('settings.context.windowInvalid')}</span>
          ) : null}
        </label>
      </section>

      <section className="space-y-4">
        <div>
          <h3 className="text-sm font-semibold text-foreground">{t('settings.context.compactTitle')}</h3>
          <p className="mt-1 text-xs leading-5 text-muted-soft">{t('settings.context.compactHint')}</p>
        </div>
        <label className="block space-y-2">
          <span className="flex items-center justify-between gap-3 text-sm">
            <span className="font-medium text-foreground">{t('settings.context.compactRatio')}</span>
            <span className="text-xs tabular-nums text-muted-soft">{ratio}%</span>
          </span>
          <Slider
            value={ratio}
            min={COMPACT_RATIO_MIN}
            max={COMPACT_RATIO_MAX}
            onValueChange={(value) => setRatioDraft(value)}
            aria-label={t('settings.context.compactRatio')}
          />
          <span className="flex justify-between text-[11px] text-faint">
            <span>{COMPACT_RATIO_MIN}%</span>
            <span>{t('settings.context.compactDefault', { value: COMPACT_RATIO_DEFAULT })}</span>
            <span>{COMPACT_RATIO_MAX}%</span>
          </span>
        </label>
      </section>

      <p className="text-xs text-faint">{t('settings.context.windowsNote')}</p>
    </div>
  )
}
