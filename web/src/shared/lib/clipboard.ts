/**
 * Copies text to the clipboard, working on both HTTPS/localhost (Clipboard API)
 * and plain HTTP LAN addresses (hidden textarea + execCommand fallback).
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (typeof navigator !== 'undefined' && navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Fall through to execCommand fallback.
    }
  }

  if (typeof document === 'undefined') return false
  const container = document.activeElement?.closest('dialog') ?? document.body
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.top = '-9999px'
  ta.style.left = '-9999px'
  container.appendChild(ta)
  ta.focus()
  ta.select()
  ta.setSelectionRange(0, text.length)
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  container.removeChild(ta)
  return ok
}
