import { defaultAppearanceConfig, type AppearanceConfig } from '@/features/appearance/api/appearance'

const APPEARANCE_CACHE_KEY = 'xlyra:appearance'

export function loadCachedAppearance(): AppearanceConfig | undefined {
  try {
    const cached = localStorage.getItem(APPEARANCE_CACHE_KEY)
    if (!cached) return undefined
    return normalizeAppearance(JSON.parse(cached))
  } catch {
    return undefined
  }
}

export function saveCachedAppearance(config: AppearanceConfig) {
  try {
    localStorage.setItem(APPEARANCE_CACHE_KEY, JSON.stringify(normalizeAppearance(config)))
  } catch {
    // 存储不可用时不影响外观查询与保存。
  }
}

function normalizeAppearance(value: unknown): AppearanceConfig | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const config = value as Partial<AppearanceConfig>
  const customImages = Array.isArray(config.custom_background_images)
    ? config.custom_background_images.filter(isStoredImage)
    : defaultAppearanceConfig.custom_background_images
  const hiddenMenuItems = Array.isArray(config.hidden_menu_items)
    ? config.hidden_menu_items.filter((item): item is string => typeof item === 'string')
    : defaultAppearanceConfig.hidden_menu_items

  return {
    background_image: isStoredImage(config.background_image) ? config.background_image : defaultAppearanceConfig.background_image,
    custom_background_images: customImages,
    hidden_menu_items: hiddenMenuItems,
    home_page: config.home_page === 'agent' || config.home_page === 'dashboard'
      ? config.home_page
      : defaultAppearanceConfig.home_page,
  }
}

function isStoredImage(value: unknown): value is string {
  return typeof value === 'string' && !value.startsWith('data:')
}
