import { useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ChartNoAxesCombined, Check, ImagePlus, LoaderCircle, RotateCcw, Save, Sparkles, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  appearanceKey,
  defaultAppearanceConfig,
  updateAppearance,
  useAppearanceQuery,
  type AppearanceConfig,
  type HomePage,
} from '@/features/appearance/api/appearance'
import { getAppNavSections, isNavItemHideable } from '@/lib/navigation'
import { drawStarfield } from '@/lib/draw-starfield'
import { convertImageToWebp } from '@/lib/image-webp'
import { useAgentAvailability } from '@/features/agent/lib/use-agent-availability'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

const MAX_BACKGROUND_IMAGE_BYTES = 8 * 1024 * 1024
const MAX_BACKGROUND_SOURCE_BYTES = 32 * 1024 * 1024

export function AppearanceSettingsPage() {
  const { t } = useTranslation('settings')
  const { t: tCommon } = useTranslation('common')
  const queryClient = useQueryClient()
  const appearanceQuery = useAppearanceQuery()
  const agentAvailable = useAgentAvailability()
  const fileInputRef = useRef<HTMLInputElement>(null)
  const saveMutation = useMutation({
    mutationFn: updateAppearance,
    onSuccess: async (config) => {
      await queryClient.invalidateQueries({ queryKey: appearanceKey })
      setDraft(config)
      toast.success(t('appearance.saved'))
    },
    onError: (error) => {
      toast.error(t('appearance.saveFailed'), { description: error.message })
    },
  })
  const [draft, setDraft] = useState<AppearanceConfig>(defaultAppearanceConfig)
  const [backgroundUploading, setBackgroundUploading] = useState(false)

  useEffect(() => {
    if (appearanceQuery.data) {
      setDraft(appearanceQuery.data)
    }
  }, [appearanceQuery.data])

  const customImages = draft.custom_background_images
  const selectedImage = draft.background_image
  const menuSections = getAppNavSections(tCommon, { agent: agentAvailable })

  function selectBackground(value: string) {
    setDraft((current) => ({ ...current, background_image: value }))
  }

  function selectHomePage(value: string) {
    const homePage: HomePage = value === 'agent' ? 'agent' : 'dashboard'
    setDraft((current) => ({ ...current, home_page: homePage }))
  }

  function toggleMenuItem(to: string, visible: boolean) {
    setDraft((current) => {
      const rest = current.hidden_menu_items.filter((item) => item !== to)
      return { ...current, hidden_menu_items: visible ? rest : [...rest, to] }
    })
  }

  function removeCustomBackground(image: string) {
    setDraft((current) => ({
      ...current,
      background_image: current.background_image === image ? '' : current.background_image,
      custom_background_images: current.custom_background_images.filter((item) => item !== image),
    }))
  }

  async function uploadBackground(file: File | undefined) {
    if (!file) return
    if (file.size > MAX_BACKGROUND_SOURCE_BYTES) {
      toast.error(t('appearance.background.imageTooLarge'))
      return
    }
    setBackgroundUploading(true)
    try {
      const convertedFile = await convertImageToWebp(file)
      if (convertedFile.size > MAX_BACKGROUND_IMAGE_BYTES) {
        toast.error(t('appearance.background.imageTooLarge'))
        return
      }
      const dataURL = await readFileAsDataURL(convertedFile)
      setDraft((current) => ({
        ...current,
        background_image: dataURL,
        custom_background_images: [...current.custom_background_images.filter((item) => item !== dataURL), dataURL],
      }))
    } catch (error) {
      toast.error(t('appearance.background.imageReadFailed'), { description: error instanceof Error ? error.message : String(error) })
    } finally {
      setBackgroundUploading(false)
    }
  }

  function handleSave() {
    saveMutation.mutate(draft)
  }

  function handleReset() {
    setDraft(appearanceQuery.data ?? defaultAppearanceConfig)
  }

  if (appearanceQuery.isLoading) {
    return <p className="text-label-2 text-body">{t('appearance.loading')}</p>
  }

  return (
    <div className="max-w-4xl space-y-8 lg:h-full lg:min-h-0 lg:overflow-y-auto lg:pr-2">
      <div className="space-y-1.5">
        <h3 className="text-title-3 text-label">{t('appearance.title')}</h3>
        <p className="text-label-2 text-body">{t('appearance.description')}</p>
      </div>

      <Tabs defaultValue="background">
        <TabsList className="max-w-xs">
          <TabsTrigger value="background">{t('appearance.tabs.background')}</TabsTrigger>
          <TabsTrigger value="menu">{t('appearance.tabs.menu')}</TabsTrigger>
        </TabsList>
        <TabsContent value="background" className="mt-4">
      <section className="space-y-4">
        <h4 className="text-headline text-label">{t('appearance.background.title')}</h4>
        <p className="text-label-2 text-body">{t('appearance.background.hint')}</p>

        <div className="overflow-hidden rounded-inner border border-separator">
          {selectedImage ? (
            <img src={selectedImage} alt="" className="aspect-[16/9] w-full object-cover object-center" />
          ) : (
            <div className="relative aspect-[16/9] w-full bg-fill-2">
              <StarfieldPreview />
              <span className="absolute inset-x-0 bottom-2 text-center text-footnote text-label-2">
                {t('appearance.background.none')}
              </span>
            </div>
          )}
        </div>

        <div className="grid grid-cols-2 items-stretch gap-3 sm:grid-cols-3">
          <button
            type="button"
            onClick={() => selectBackground('')}
            className={cn(
              'group relative overflow-hidden rounded-inner border text-left transition-colors',
              selectedImage === '' ? 'border-tint' : 'border-separator hover:border-label-3',
            )}
          >
            <span className="relative block aspect-[16/9] w-full bg-fill-2">
              <StarfieldPreview />
            </span>
            <span className="block px-3 py-2 text-footnote text-label-2">{t('appearance.background.none')}</span>
            {selectedImage === '' ? <SelectedMark /> : null}
          </button>

          {customImages.map((image, index) => (
            <div
              key={image}
              className={cn(
                'relative overflow-hidden rounded-inner border text-left transition-colors',
                selectedImage === image ? 'border-tint' : 'border-separator hover:border-label-3',
              )}
            >
              <button type="button" onClick={() => selectBackground(image)} className="block w-full text-left">
                <img src={image} alt="" className="aspect-[16/9] w-full object-cover object-center" />
                <span className="block px-3 py-2 text-footnote text-label-2">
                  {t('appearance.background.custom', { index: index + 1 })}
                </span>
              </button>
              {selectedImage === image ? <SelectedMark /> : null}
              <button
                type="button"
                onClick={(event) => {
                  event.stopPropagation()
                  removeCustomBackground(image)
                }}
                className="absolute left-2 top-2 flex h-6 w-6 items-center justify-center rounded-full bg-black/65 text-white transition-colors hover:bg-black/85"
                aria-label={t('appearance.background.remove', { index: index + 1 })}
              >
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          ))}

          <div className="relative h-full min-h-0">
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={backgroundUploading}
              className="flex h-full min-h-[110px] w-full flex-col items-center justify-center gap-2 rounded-inner border border-dashed border-separator text-footnote text-label-2 transition-colors hover:border-label-3 hover:text-label"
            >
              {backgroundUploading ? <LoaderCircle className="h-5 w-5 animate-spin" /> : <ImagePlus className="h-5 w-5" />}
              <span>{t('appearance.background.upload')}</span>
            </button>
          </div>
          <input
            ref={fileInputRef}
            type="file"
            accept="image/png,image/jpeg,image/webp"
            disabled={backgroundUploading}
            className="hidden"
            onChange={(event) => {
              void uploadBackground(event.target.files?.[0])
              event.target.value = ''
            }}
          />
        </div>
      </section>
        </TabsContent>
        <TabsContent value="menu" className="mt-4 space-y-8">
          <section className="space-y-4">
            <h4 className="text-headline text-label">{t('appearance.menu.homeTitle')}</h4>
            <p className="text-label-2 text-body">{t('appearance.menu.homeHint')}</p>
            <div className="grid max-w-md grid-cols-2 gap-3">
              {[
                { value: 'dashboard', label: t('appearance.menu.homeDashboard'), icon: ChartNoAxesCombined },
                ...(agentAvailable ? [{ value: 'agent', label: t('appearance.menu.homeAgent'), icon: Sparkles }] : []),
              ].map(({ value, label, icon: Icon }) => (
                <button
                  key={value}
                  type="button"
                  onClick={() => selectHomePage(value)}
                  className={cn(
                    'relative flex flex-col items-center gap-2 rounded-inner border px-4 py-5 text-footnote transition-colors',
                    draft.home_page === value ? 'border-tint text-label' : 'border-separator text-label-2 hover:border-label-3',
                  )}
                >
                  <Icon className="h-6 w-6" />
                  <span>{label}</span>
                  {draft.home_page === value ? <SelectedMark /> : null}
                </button>
              ))}
            </div>
          </section>

          <section className="space-y-4 border-t border-separator pt-8">
            <h4 className="text-headline text-label">{t('appearance.menu.itemsTitle')}</h4>
            <p className="text-label-2 text-body">{t('appearance.menu.itemsHint')}</p>
            {menuSections.map((section) => (
              <div key={section.key} className="space-y-2">
                <p className="text-footnote font-semibold text-label-3">{section.label}</p>
                <div className="grid gap-2">
                  {section.items.map((item) => {
                    const hideable = isNavItemHideable(item.to)
                    const children = item.children ?? []
                    return (
                      <div key={item.to} className="space-y-2">
                        <Switch
                          label={item.label}
                          checked={!draft.hidden_menu_items.includes(item.to)}
                          disabled={!hideable}
                          onCheckedChange={(visible) => toggleMenuItem(item.to, visible)}
                        />
                        {children.map((child) => (
                          <Switch
                            key={child.to}
                            label={child.label}
                            className="ml-6"
                            checked={!draft.hidden_menu_items.includes(child.to)}
                            disabled={!isNavItemHideable(child.to)}
                            onCheckedChange={(visible) => toggleMenuItem(child.to, visible)}
                          />
                        ))}
                      </div>
                    )
                  })}
                </div>
              </div>
            ))}
          </section>
        </TabsContent>
      </Tabs>

      <div className="flex items-center gap-3 border-t border-separator pt-6">
        <Button onClick={handleSave} disabled={saveMutation.isPending}>
          {saveMutation.isPending ? <LoaderCircle className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
          {t('common:actions.save', { ns: 'common' })}
        </Button>
        <Button variant="secondary" onClick={handleReset} disabled={saveMutation.isPending || appearanceQuery.isFetching}>
          <RotateCcw className="h-4 w-4" />
          {t('common:actions.reset', { ns: 'common' })}
        </Button>
      </div>
    </div>
  )
}

function SelectedMark() {
  return (
    <span className="absolute right-2 top-2 grid h-5 w-5 place-items-center rounded-full bg-tint text-white">
      <Check className="h-3.5 w-3.5" />
    </span>
  )
}

/** 图库「无」格与顶部预览的星点示意：复用全局星点绘制，只在挂载时画一次 */
function StarfieldPreview() {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const draw = () => drawStarfield(canvas)
    draw()
    const timer = window.setTimeout(draw, 120)
    return () => window.clearTimeout(timer)
  }, [])

  return (
    <canvas
      ref={canvasRef}
      aria-hidden="true"
      className="pointer-events-none absolute inset-0 size-full"
      style={{ backgroundColor: 'var(--bg-base)' }}
    />
  )
}

function readFileAsDataURL(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error ?? new Error('read failed'))
    reader.readAsDataURL(file)
  })
}
