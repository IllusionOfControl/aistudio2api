import { onActivated, onUnmounted, ref, watch, type Ref } from 'vue'

// queryValue reads address bar query parameter constrained to allowed values
function queryValue<T extends string>(name: string, allowed: readonly T[], fallback: T): T {
  const value = new URLSearchParams(window.location.search).get(name) as T | null
  return value !== null && allowed.includes(value) ? value : fallback
}

// writeQuery replaces address bar query parameters without generating new history entries
function writeQuery(values: Record<string, string | null>): void {
  const url = new URL(window.location.href)
  for (const [name, value] of Object.entries(values)) {
    if (value === null) url.searchParams.delete(name)
    else url.searchParams.set(name, value)
  }
  if (url.href !== window.location.href) window.history.replaceState(null, '', url)
}

// useQueryParam syncs in-page state with address bar query params, updating on navigation
export function useQueryParam<T extends string>(
  name: string,
  allowed: readonly T[],
  fallback: T,
): Ref<T> {
  const state = ref(queryValue(name, allowed, fallback)) as Ref<T>
  const write = (value: T): void => writeQuery({ [name]: value === fallback ? null : value })
  watch(state, write)
  onActivated(() => write(state.value))
  const sync = (): void => {
    state.value = queryValue(name, allowed, fallback)
  }
  window.addEventListener('popstate', sync)
  onUnmounted(() => window.removeEventListener('popstate', sync))
  return state
}

// useTabParam stores current page as tab query parameter, clearing previous query parameters on tab switch
export function useTabParam<T extends string>(
  allowed: readonly T[],
  fallback: T,
): { tab: Ref<T>; select: (tab: T) => void } {
  const tab = ref(queryValue('tab', allowed, fallback)) as Ref<T>
  const sync = (): void => {
    tab.value = queryValue('tab', allowed, fallback)
  }
  window.addEventListener('popstate', sync)
  onUnmounted(() => window.removeEventListener('popstate', sync))
  const select = (next: T): void => {
    if (next === tab.value) return
    tab.value = next
    const url = new URL(window.location.href)
    url.search = ''
    if (next !== fallback) url.searchParams.set('tab', next)
    window.history.pushState(null, '', url)
  }
  return { tab, select }
}
