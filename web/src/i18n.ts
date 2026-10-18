import { readonly, ref } from 'vue'
import { ApiError } from '@/api'
import * as de from '@/locales/de'
import * as en from '@/locales/en'
import * as fr from '@/locales/fr'
import * as ja from '@/locales/ja'
import * as ko from '@/locales/ko'
import * as zhCN from '@/locales/zh-CN'
import type { LogPhrase, TranslationKey } from '@/locales/zh-CN'
import * as zhTW from '@/locales/zh-TW'
import type { Locale, UpstreamChannel } from '@/types'

export type { TranslationKey }

// catalogs maps locales to their label, messages, and log phrases
const catalogs: Record<
  Locale,
  { label: string; messages: Record<TranslationKey, string>; logs: Record<LogPhrase, string> }
> = { 'zh-CN': zhCN, 'zh-TW': zhTW, en, ja, ko, fr, de }

export const availableLocales = (Object.keys(catalogs) as Locale[]).map((code) => ({
  code,
  label: catalogs[code].label,
}))

const savedLocale = window.localStorage.getItem('user_lang') as Locale | null
const locale = ref<Locale>(
  savedLocale !== null && Object.hasOwn(catalogs, savedLocale) ? savedLocale : 'zh-CN',
)
document.documentElement.lang = locale.value

// channelLabelKey returns translation key for upstream channels
export function channelLabelKey(channel: UpstreamChannel): TranslationKey {
  if (channel === 'assistant') return 'channel.assistant'
  return channel === 'build' ? 'channel.build' : 'channel.playground'
}

// useI18n provides UI translations, error text, and log phrase localization for active locale
export function useI18n() {
  const t = (key: TranslationKey): string => catalogs[locale.value].messages[key]
  const setLocale = (value: Locale): void => {
    locale.value = value
    document.documentElement.lang = value
    window.localStorage.setItem('user_lang', value)
  }

  // errorText returns error message for UI: shows offline when connection fails, maps error codes to active locale
  const errorText = (reason: unknown): string => {
    if (reason instanceof TypeError) return t('app.offline')
    if (reason instanceof ApiError) {
      const key = `error.${reason.code}`
      if (Object.hasOwn(zhCN.messages, key)) {
        return t(key as TranslationKey).replace('{detail}', reason.message)
      }
    }
    return reason instanceof Error ? reason.message : t('common.error')
  }

  // logText translates fixed phrases in "stage | key=value" logs to active locale, preserving data and error text
  const logText = (message: string): string => {
    if (locale.value === 'zh-CN') return message
    const table: Record<string, string> = catalogs[locale.value].logs
    const phrase = (text: string): string => (Object.hasOwn(table, text) ? table[text]! : text)
    return message
      .split('\n')
      .map((line) => {
        if (line.startsWith('原因: ')) return `${phrase('原因')}: ${line.slice(4)}`
        return line
          .split(' | ')
          .map((segment) => {
            if (Object.hasOwn(table, segment)) return table[segment]
            const separator = segment.indexOf('=')
            if (separator <= 0) return segment
            return `${phrase(segment.slice(0, separator))}=${phrase(segment.slice(separator + 1))}`
          })
          .join(' | ')
      })
      .join('\n')
  }

  return { availableLocales, locale: readonly(locale), setLocale, t, errorText, logText }
}
