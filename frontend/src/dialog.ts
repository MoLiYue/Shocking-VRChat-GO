// In-app replacements for window.confirm / alert.
// Native dialogs in WebView2 show "127.0.0.1 says…" and break the app feel.
import { reactive } from 'vue'

export interface ConfirmOptions {
  message: string
  title?: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
}

interface ConfirmState extends ConfirmOptions {
  open: boolean
  resolve: ((ok: boolean) => void) | null
}

export interface Toast {
  id: number
  message: string
  type: 'info' | 'success' | 'error'
}

export const dialogState = reactive<{ confirm: ConfirmState; toasts: Toast[] }>({
  confirm: { open: false, message: '', resolve: null },
  toasts: [],
})

/** Show a modal confirm dialog; resolves true when confirmed. */
export function confirmDialog(opts: ConfirmOptions | string): Promise<boolean> {
  const o = typeof opts === 'string' ? { message: opts } : opts
  // A second confirm while one is open cancels the first.
  dialogState.confirm.resolve?.(false)
  return new Promise((resolve) => {
    Object.assign(dialogState.confirm, { title: undefined, confirmText: undefined, cancelText: undefined, danger: false }, o, {
      open: true,
      resolve,
    })
  })
}

export function closeConfirm(ok: boolean) {
  const r = dialogState.confirm.resolve
  dialogState.confirm.open = false
  dialogState.confirm.resolve = null
  r?.(ok)
}

let toastSeq = 0

/** Show a transient notification in the corner. */
export function toast(message: string, type: Toast['type'] = 'info', ms = 2600) {
  const id = ++toastSeq
  dialogState.toasts.push({ id, message, type })
  setTimeout(() => {
    const i = dialogState.toasts.findIndex((t) => t.id === id)
    if (i >= 0) dialogState.toasts.splice(i, 1)
  }, ms)
}
