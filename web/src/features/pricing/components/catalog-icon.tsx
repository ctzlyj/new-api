import { getLobeIcon } from '@/lib/lobe-icon'

const QINIU_MODEL_ICON_PATH = '/ai-inference/model-icons/'

function getTrustedCatalogIconUrl(icon: string): string | null {
  try {
    const url = new URL(icon)
    if (
      url.protocol === 'https:' &&
      url.hostname === 'static.qiniu.com' &&
      url.pathname.startsWith(QINIU_MODEL_ICON_PATH)
    ) {
      return url.toString()
    }
  } catch {
    return null
  }
  return null
}

export function CatalogIcon(props: {
  icon?: string | null
  size: number
  className?: string
}) {
  const icon = props.icon?.trim()
  if (!icon) return null

  const trustedUrl = getTrustedCatalogIconUrl(icon)
  if (trustedUrl) {
    return (
      <img
        src={trustedUrl}
        alt=''
        aria-hidden='true'
        width={props.size}
        height={props.size}
        loading='lazy'
        referrerPolicy='no-referrer'
        className={props.className ?? 'object-contain'}
      />
    )
  }
  if (/^[a-z][a-z\d+.-]*:/i.test(icon)) return null
  return getLobeIcon(icon, props.size)
}
