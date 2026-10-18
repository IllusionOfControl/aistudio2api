import { readonly, ref } from 'vue'

// Theme is the UI color scheme mode
export type Theme = 'dark' | 'light'

// theme initializes from index.html setting based on localStorage before the first frame
const theme = ref<Theme>(document.documentElement.dataset.theme === 'light' ? 'light' : 'dark')

// applyTheme toggles the root element's theme and persists it
function applyTheme(next: Theme): void {
  theme.value = next
  document.documentElement.dataset.theme = next
  window.localStorage.setItem('user_theme', next)
}

// useTheme provides current UI theme and toggle method, animating circular reveal/collapse from origin
export function useTheme() {
  const toggleTheme = (origin: { x: number; y: number }): void => {
    const next: Theme = theme.value === 'dark' ? 'light' : 'dark'
    const root = document.documentElement
    // themeSwitching disables element transitions during theme change so all elements update in a single frame
    root.dataset.themeSwitching = ''
    const finish = (): void => {
      delete root.dataset.themeSwitching
    }
    if (
      !('startViewTransition' in document) ||
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
    ) {
      applyTheme(next)
      void getComputedStyle(root).color
      finish()
      return
    }
    const radius = Math.hypot(
      Math.max(origin.x, window.innerWidth - origin.x),
      Math.max(origin.y, window.innerHeight - origin.y),
    )
    const circle = [
      `circle(0px at ${origin.x}px ${origin.y}px)`,
      `circle(${radius}px at ${origin.x}px ${origin.y}px)`,
    ]
    const transition = document.startViewTransition(() => applyTheme(next))
    void transition.ready.then(() => {
      const reveal = root.animate(
        { clipPath: next === 'light' ? circle : [...circle].reverse() },
        {
          duration: 420,
          easing: next === 'light' ? 'ease-out' : 'ease-in',
          fill: 'forwards',
          pseudoElement: `::view-transition-${next === 'light' ? 'new' : 'old'}(root)`,
        },
      )
      void transition.finished.finally(() => reveal.cancel())
    })
    void transition.finished.finally(finish)
  }

  return { theme: readonly(theme), toggleTheme }
}
