import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from '../components/icons'
import { BackupCard } from '../components/BackupCard'
import { ImportPreviewDialog } from '../components/ImportPreviewDialog'

type Props = {
  onDone: () => void
}

export function ImportPage({ onDone }: Props) {
  const { t } = useTranslation()
  const [format, setFormat] = useState<'netscape' | 'json'>('netscape')
  const [file, setFile] = useState<File | null>(null)
  const [previewing, setPreviewing] = useState(false)

  return (
    <div style={{ padding: 6, maxWidth: 1280 }}>
      <div className="fx-pagehead" style={{ marginBottom: 18 }}>
        <div>
          <div className="fx-pagehead-kicker">{t('import.page_kicker')}</div>
          <h1 className="fx-pagehead-h">{t('import.page_title')}</h1>
        </div>
      </div>

      <div className="fx-importpage-grid">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>

      <section className="fx-card" style={{ marginBottom: 16 }}>
        <div className="fx-card-body" style={{ gap: 14, padding: 18 }}>
          <h3 className="fx-card-title" style={{ fontSize: 16 }}>{t('import.import_section_title')}</h3>

          <div className="fx-segment" role="group" aria-label="format">
            <button
              className={'fx-seg' + (format === 'netscape' ? ' fx-seg-active' : '')}
              onClick={() => setFormat('netscape')}
              aria-pressed={format === 'netscape'}
              data-tooltip={t('import.format_bookmarks_tooltip')}
            >
              {t('import.format_bookmarks_label')}
            </button>
            <button
              className={'fx-seg' + (format === 'json' ? ' fx-seg-active' : '')}
              onClick={() => setFormat('json')}
              aria-pressed={format === 'json'}
              data-tooltip={t('import.format_json_tooltip')}
            >
              {t('import.format_json_label')}
            </button>
          </div>
          <div style={{ fontSize: 11, color: 'var(--fx-ink-4)' }}>
            {format === 'netscape'
              ? t('import.format_hint_bookmarks')
              : t('import.format_hint_json')}
          </div>

          <div
            style={{
              border: '1.5px dashed var(--fx-border)',
              borderRadius: 12,
              padding: 28,
              textAlign: 'center',
              cursor: 'pointer',
              background: 'var(--fx-surface)',
            }}
            onDragOver={(e) => e.preventDefault()}
            onDrop={(e) => {
              e.preventDefault()
              const f = e.dataTransfer.files?.[0]
              if (f) setFile(f)
            }}
            onClick={() => document.getElementById('foldex-file')?.click()}
          >
            <Icon d={I.upload} size={28} />
            <div style={{ marginTop: 8, color: 'var(--fx-ink-3)' }}>
              {file ? file.name : t('import.drop_zone')}
            </div>
            <input
              type="file"
              id="foldex-file"
              hidden
              accept={format === 'netscape' ? '.html,.htm' : '.json'}
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
          </div>

          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <button
              className="fx-cta fx-cta-fill"
              disabled={!file}
              onClick={() => setPreviewing(true)}
            >
              {t('import.review_button')}
              <Icon d={I.arrowR} size={15} stroke={2} />
            </button>
            <span style={{ fontSize: 11, color: 'var(--fx-ink-4)' }}>
              {t('import.review_hint')}
            </span>
          </div>
        </div>
      </section>

      <section className="fx-card">
        <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
          <h3 className="fx-card-title" style={{ fontSize: 16 }}>{t('import.export_section_title')}</h3>
          <div style={{ display: 'flex', gap: 8 }}>
            <a
              className="fx-pillbtn"
              href="/api/export?format=netscape"
              data-tooltip={t('import.export_bookmarks_tooltip')}
            >
              <Icon d={I.upload} size={13} /> {t('import.format_bookmarks_label')}
            </a>
            <a
              className="fx-pillbtn"
              href="/api/export?format=json"
              data-tooltip={t('import.export_json_tooltip')}
            >
              <Icon d={I.upload} size={13} /> JSON
            </a>
          </div>
        </div>
      </section>
        </div>

        <div>
          <BackupCard onRestored={onDone} />
        </div>
      </div>

      {previewing && file && (
        <ImportPreviewDialog
          file={file}
          format={format}
          onClose={() => setPreviewing(false)}
          onApplied={() => {
            setPreviewing(false)
            setFile(null)
            onDone()
          }}
        />
      )}
    </div>
  )
}
