import { useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Check, ImagePlus, LoaderCircle, RotateCcw, Save, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  appearanceKey,
  defaultAppearanceConfig,
  updateAppearance,
  useAppearanceQuery,
  type AppearanceConfig,
} from '@/features/appearance/api/appearance'
import { drawStarfield } from '@/lib/draw-starfield'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

const MAX_BACKGROUND_IMAGE_BYTES = 8 * 1024 * 1024

export function AppearanceSettingsPage() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const appearanceQuery = useAppearanceQuery()
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

  useEffect(() => {
    if (appearanceQuery.data) {
      setDraft(appearanceQuery.data)
    }
  }, [appearanceQuery.data])

  const customImages = draft.custom_background_images
  const selectedImage = draft.background_image

  function selectBackground(value: string) {
    setDraft((current) => ({ ...current, background_image: value }))
  }

  function removeCustomBackground(image: string) {
    setDraft((current) => ({
      background_image: current.background_image === image ? '' : current.background_image,
      custom_background_images: current.custom_background_images.filter((item) => item !== image),
    }))
  }

  async function uploadBackground(file: File | undefined) {
    if (!file) return
    if (file.size > MAX_BACKGROUND_IMAGE_BYTES) {
      toast.error(t('appearance.background.imageTooLarge'))
      return
    }
    try {
      const dataURL = await readFileAsDataURL(file)
      setDraft((current) => ({
        background_image: dataURL,
        custom_background_images: [...current.custom_background_images.filter((item) => item !== dataURL), dataURL],
      }))
    } catch (error) {
      toast.error(t('appearance.background.imageReadFailed'), { description: error instanceof Error ? error.message : String(error) })
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
              className="flex h-full min-h-[110px] w-full flex-col items-center justify-center gap-2 rounded-inner border border-dashed border-separator text-footnote text-label-2 transition-colors hover:border-label-3 hover:text-label"
            >
              <ImagePlus className="h-5 w-5" />
              <span>{t('appearance.background.upload')}</span>
            </button>
          </div>
          <input
            ref={fileInputRef}
            type="file"
            accept="image/png,image/jpeg,image/webp"
            className="hidden"
            onChange={(event) => {
              void uploadBackground(event.target.files?.[0])
              event.target.value = ''
            }}
          />
        </div>
      </section>

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
