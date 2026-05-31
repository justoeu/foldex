import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { useSubscribePush, useUnsubscribePush, useWebPush } from '../hooks/useWebPush'

// Topbar bell that flips push subscription on/off. Three visible states:
//   - unsupported / permission denied → muted bell, click is no-op (tooltip
//     explains)
//   - supported + not subscribed       → outline bell, click subscribes
//   - subscribed                       → filled-style bell, click unsubscribes
// Kept as a small standalone component so the Topbar doesn't grow another
// branchy block — and so adding a settings page later just relocates this
// file instead of carving it out of a 350-line topbar.
export function PushToggle() {
  const { t } = useTranslation()
  const status = useWebPush()
  const subscribe = useSubscribePush()
  const unsubscribe = useUnsubscribePush()

  if (!status.data) {
    // Still resolving — render an inert placeholder so the layout doesn't
    // shift in.
    return (
      <button
        className="fx-themetoggle"
        aria-busy="true"
        aria-label={t('topbar.push_subscribe')}
        disabled
      >
        <Icon d={I.bell} size={16} />
      </button>
    )
  }

  if (!status.data.supported) {
    return (
      <button
        className="fx-themetoggle"
        aria-label={t('topbar.push_unsupported')}
        data-tooltip={t('topbar.push_unsupported')}
        disabled
      >
        <Icon d={I.bellOff} size={16} />
      </button>
    )
  }

  if (status.data.permission === 'denied') {
    return (
      <button
        className="fx-themetoggle"
        aria-label={t('topbar.push_denied')}
        data-tooltip={t('topbar.push_denied')}
        disabled
      >
        <Icon d={I.bellOff} size={16} />
      </button>
    )
  }

  const subscribed = status.data.subscribed
  const onClick = () => {
    if (subscribed) unsubscribe.mutate()
    else subscribe.mutate()
  }
  return (
    <button
      className={'fx-themetoggle' + (subscribed ? ' fx-themetoggle-active' : '')}
      aria-label={subscribed ? t('topbar.push_unsubscribe') : t('topbar.push_subscribe')}
      data-tooltip={subscribed ? t('topbar.push_unsubscribe') : t('topbar.push_subscribe')}
      aria-pressed={subscribed}
      disabled={subscribe.isPending || unsubscribe.isPending}
      onClick={onClick}
    >
      <Icon d={I.bell} size={16} />
    </button>
  )
}
