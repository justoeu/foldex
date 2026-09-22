import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'

/**
 * The Chrome extension distribution card.
 *
 * The zip is embedded in the server binary and served by an admin-only
 * route, so the card fetches nothing at mount: the version is not known to
 * the SPA (it travels in the download's filename and headers), and a probe
 * request would only repeat what the download itself says. All it owes the
 * administrator is the file and the three steps it takes to load it.
 */
export function AddonCard() {
  const { t } = useTranslation()

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3
          className="fx-card-title"
          style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}
        >
          <Icon d={I.download} size={15} /> {t('admin.addon_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>
          {t('admin.addon_desc')}
        </p>

        {/* An anchor, not a button: the download needs a real href so the
            session cookie rides along and Content-Disposition names the
            file — a JS navigation would drop both. */}
        <div>
          <a className="fx-btn fx-btn-primary" href="/api/admin/addon/download">
            <Icon d={I.download} size={13} /> {t('admin.addon_download')}
          </a>
        </div>
        <p style={{ fontSize: 11, color: 'var(--fx-ink-4)', margin: 0 }}>
          {t('admin.addon_version_note')}
        </p>

        <div style={{ display: 'grid', gap: 6 }}>
          <strong style={{ fontSize: 12 }}>{t('admin.addon_steps_title')}</strong>
          <ol style={{ margin: 0, paddingLeft: 18, display: 'grid', gap: 4, fontSize: 12 }}>
            <li>{t('admin.addon_step1')}</li>
            <li>
              <code style={{ fontFamily: 'var(--fx-mono)', fontSize: 11 }}>chrome://extensions</code>
            </li>
            <li>{t('admin.addon_step3')}</li>
          </ol>
        </div>

        <p style={{ fontSize: 11, color: 'var(--fx-ink-4)', margin: 0 }}>
          {t('admin.addon_permissions')}
        </p>
      </div>
    </section>
  )
}
