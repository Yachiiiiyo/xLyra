import { APIError } from '@/lib/http'
import { sanitizeErrorText } from './sanitize-error'

const INSTALL_ERRORS: Record<string, string> = {
  js_plugin_market_incompatible: 'jsPlugins.market.installError.incompatible',
  js_plugin_market_revoked: 'jsPlugins.market.installError.revoked',
  js_plugin_market_revocations_expired: 'jsPlugins.market.installError.revocationsExpired',
  js_plugin_market_developer_mismatch: 'jsPlugins.market.installError.developerMismatch',
  js_plugin_market_download_failed: 'jsPlugins.market.installError.downloadFailed',
  js_plugin_market_verify_failed: 'jsPlugins.market.installError.verifyFailed',
  js_plugin_market_unsigned: 'jsPlugins.market.installError.unsigned',
  js_plugin_market_selftest_failed: 'jsPlugins.market.installError.selftestFailed',
  js_plugin_package_invalid: 'jsPlugins.market.installError.packageInvalid',
  js_plugin_version_conflict: 'jsPlugins.market.installError.versionConflict',
  js_plugin_market_not_found: 'jsPlugins.market.installError.notFound',
  js_plugin_market_version_not_found: 'jsPlugins.market.installError.notFound',
  js_plugin_market_unavailable: 'jsPlugins.market.installError.unavailable',
}

export function marketInstallErrorText(t: (key: string) => string, error: unknown) {
  if (error instanceof APIError) {
    const key = error.code ? INSTALL_ERRORS[error.code] : undefined
    if (key) return t(key)
    return sanitizeErrorText(error.message)
  }
  if (error instanceof Error) return sanitizeErrorText(error.message)
  return sanitizeErrorText('')
}
