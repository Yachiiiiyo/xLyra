import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/http'

export type AppearanceConfig = {
  /** 空串 = 不用背景图（星点背景）；否则为已存储的背景图路径 */
  background_image: string
  custom_background_images: string[]
}

export const defaultAppearanceConfig: AppearanceConfig = {
  background_image: '',
  custom_background_images: [],
}

export const appearanceKey = ['settings', 'appearance'] as const

export async function fetchAppearance(): Promise<AppearanceConfig> {
  return apiFetch<AppearanceConfig>('/api/v1/settings/appearance')
}

export async function updateAppearance(config: AppearanceConfig) {
  return apiFetch<AppearanceConfig>('/api/v1/settings/appearance', {
    method: 'PUT',
    body: config,
  })
}

/** 全局外观查询：失败或加载中都按「无背景」处理，不报错、不阻塞页面 */
export function useAppearanceQuery() {
  return useQuery({
    queryKey: appearanceKey,
    queryFn: fetchAppearance,
    staleTime: 60_000,
    retry: false,
  })
}

export function useAppBackgroundImage() {
  const { data } = useAppearanceQuery()
  return data?.background_image?.trim() || ''
}

/** 是否配置了背景图：加载中 / 失败按「未配置」处理，供玻璃面板切换 pane 外观 */
export function useHasAppBackground() {
  return Boolean(useAppBackgroundImage())
}
