export type ToastType = 'success' | 'error' | 'info'
export interface Toast { id: string; message: string; type: ToastType }

export const toast = {
  success: (message: string) => dispatchToast(message, 'success'),
  error: (message: string) => dispatchToast(message, 'error'),
  info: (message: string) => dispatchToast(message, 'info'),
}

function dispatchToast(message: string, type: ToastType) {
  window.dispatchEvent(new CustomEvent('toast', { detail: { id: Date.now().toString(), message, type } }))
}
