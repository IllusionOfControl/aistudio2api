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

// bindQuery synchronizes in-page state with address bar query parameters.
// Does not write when equal to fallback, updates on browser forward/back navigation, and restores on cached page reactivate.
function bindQuery<T extends string>(name: string, read: () => T, fallback: T): Ref<T> {
  const state = ref(read()) as Ref<T>
  const write = (value: T): void => writeQuery({ [name]: value === fallback ? null : value })
  watch(state, write)
  onActivated(() => write(state.value))
  const sync = (): void => {
    state.value = read()
  }
  window.addEventListener('popstate', sync)
  onUnmounted(() => window.removeEventListener('popstate', sync))
  return state
}

// useQueryParam stores in-page state using restricted query parameter values.
export function useQueryParam<T extends string>(
  name: string,
  allowed: readonly T[],
  fallback: T,
): Ref<T> {
  return bindQuery(name, () => queryValue(name, allowed, fallback), fallback)
}

// useQueryText stores in-page state using arbitrary text query parameters.
export function useQueryText(name: string, fallback = ''): Ref<string> {
  return bindQuery(
    name,
    () => new URLSearchParams(window.location.search).get(name) ?? fallback,
    fallback,
  )
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
