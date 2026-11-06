import { useQuery } from '@tanstack/react-query'
import { loadCachedAppearance, saveCachedAppearance } from '@/features/appearance/lib/appearance-cache'
import { apiFetch } from '@/lib/http'

export type HomePage = 'dashboard' | 'agent'

export type AppearanceConfig = {
  /** 空串 = 不用背景图（星点背景）；否则为已存储的背景图路径 */
  background_image: string
  custom_background_images: string[]
  /** 被隐藏菜单项的 to 路径 */
  hidden_menu_items: string[]
  /** 程序首页：访问 / 时跳转的目标 */
  home_page: HomePage
}

export const defaultAppearanceConfig: AppearanceConfig = {
  background_image: '',
  custom_background_images: [],
  hidden_menu_items: [],
  home_page: 'dashboard',
}

export const appearanceKey = ['settings', 'appearance'] as const

export async function fetchAppearance(): Promise<AppearanceConfig> {
  const config = await apiFetch<AppearanceConfig>('/api/v1/settings/appearance')
  saveCachedAppearance(config)
  return config
}

export async function updateAppearance(config: AppearanceConfig) {
  const updated = await apiFetch<AppearanceConfig>('/api/v1/settings/appearance', {
    method: 'PUT',
    body: config,
  })
  saveCachedAppearance(updated)
  return updated
}

/** 全局外观查询：失败或加载中都按「无背景」处理，不报错、不阻塞页面 */
export function useAppearanceQuery() {
  return useQuery({
    queryKey: appearanceKey,
    queryFn: fetchAppearance,
    initialData: loadCachedAppearance(),
    initialDataUpdatedAt: 0,
    staleTime: 60_000,
    retry: false,
  })
}

export function useAppBackgroundImage() {
  const { data } = useAppearanceQuery()
  return data?.background_image?.trim() || ''
}

export type MenuPreferences = {
  hiddenMenuItems: string[]
  homePage: HomePage
}

/** 菜单偏好：加载中 / 失败用默认值（全部显示、首页 dashboard） */
export function useMenuPreferences(): MenuPreferences {
  const { data } = useAppearanceQuery()
  return {
    hiddenMenuItems: data?.hidden_menu_items ?? defaultAppearanceConfig.hidden_menu_items,
    homePage: data?.home_page ?? defaultAppearanceConfig.home_page,
  }
}

/** 是否配置了背景图：加载中 / 失败按「未配置」处理，供玻璃面板切换 pane 外观 */
export function useHasAppBackground() {
  return Boolean(useAppBackgroundImage())
}
