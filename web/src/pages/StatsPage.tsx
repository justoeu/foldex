import { useMemo, useState, type CSSProperties } from 'react'
import { Icon, I } from '../components/icons'
import {
  useStatsDaily,
  useStatsStorage,
  useStatsSummary,
  useStatsTags,
  useStatsTop,
  type DailyPoint,
  type TopLink,
} from '../api/stats'

export function StatsPage() {
  const summary = useStatsSummary()
  const daily = useStatsDaily(60)
  const top = useStatsTop(5)
  const tagBuckets = useStatsTags()
  const storage = useStatsStorage()

  const s = summary.data
  const mom = useMemo(() => {
    if (!s) return 0
    if (s.clicks_prev_30d === 0) return s.clicks_last_30d > 0 ? 100 : 0
    return Math.round(((s.clicks_last_30d - s.clicks_prev_30d) / s.clicks_prev_30d) * 100)
  }, [s])

  const totalDaily = daily.data?.reduce((acc, p) => acc + p.clicks, 0) ?? 0
  const clicksPerLink = s && s.total_links > 0 ? (s.clicks_last_30d / s.total_links).toFixed(1) : '—'

  return (
    <div className="fx-stats">
      <div className="fx-stats-head">
        <div>
          <div className="fx-pagehead-kicker">📊 ANALYTICS · /go telemetry</div>
          <h1 className="fx-pagehead-h">Estatísticas</h1>
          <div className="fx-stats-sub">
            Cliques rastreados via redirect <span className="fx-mono-inline">/go/:id</span>
          </div>
        </div>
      </div>

      <div className="fx-kpis">
        <KpiCard
          label="Cliques · 30d"
          value={s ? s.clicks_last_30d.toLocaleString('pt-BR') : '—'}
          delta={s ? (mom >= 0 ? '+' : '') + mom + '%' : ''}
          deltaKind={mom >= 0 ? 'up' : 'down'}
          spark={daily.data?.slice(-14).map((p) => p.clicks)}
        />
        <KpiCard
          label="Links totais"
          value={s ? s.total_links : '—'}
          delta={s ? `+${s.new_links_last_30d} novos · 30d` : ''}
          deltaKind="up"
        />
        <KpiCard
          label="Cliques · link"
          value={clicksPerLink}
          delta={s ? `${s.clicks_last_30d} cliques / ${s.total_links} links` : ''}
          deltaKind="neutral"
        />
        <KpiCard
          label="Top host"
          value={s && s.top_host ? s.top_host : '—'}
          valueClass="fx-kpi-host"
          delta={s ? `${s.top_host_clicks} cliques totais` : ''}
          deltaKind="neutral"
        />
        <KpiCard
          label="Objects stored"
          value={storage.data ? storage.data.objects.toLocaleString('pt-BR') : '—'}
          delta={storage.data ? formatBytes(storage.data.total_bytes) + ' · MinIO' : 'MinIO indisponível'}
          deltaKind="neutral"
        />
      </div>

      <div className="fx-stats-grid">
        <section className="fx-statcard fx-statcard-wide">
          <header className="fx-statcard-head">
            <div>
              <div className="fx-statcard-title">Cliques diários</div>
              <div className="fx-statcard-sub">
                Últimos 60 dias · total {totalDaily.toLocaleString('pt-BR')}
              </div>
            </div>
            <div className="fx-statcard-legend">
              <span>
                <span className="fx-legend-dot" style={{ background: 'var(--fx-accent)' }} /> Cliques
              </span>
              <span>
                <span className="fx-legend-dot fx-legend-dot-line" /> Média 7d
              </span>
            </div>
          </header>
          {daily.data && daily.data.length > 0 ? (
            <AreaChart data={daily.data} width={760} height={220} />
          ) : (
            <EmptyChart hint="Sem cliques nos últimos 60 dias" />
          )}
        </section>

        <section className="fx-statcard">
          <header className="fx-statcard-head">
            <div>
              <div className="fx-statcard-title">Mês vs mês</div>
              <div className="fx-statcard-sub">Δ cliques · base 30d ant.</div>
            </div>
            <div className="fx-mom-pill">
              <Icon d={I.flame} size={11} /> {mom >= 0 ? '+' : ''}
              {mom}%
            </div>
          </header>
          {s ? <MomCompare prev={s.clicks_prev_30d} curr={s.clicks_last_30d} /> : null}
        </section>

        <section className="fx-statcard">
          <header className="fx-statcard-head">
            <div>
              <div className="fx-statcard-title">Top links · 30d</div>
              <div className="fx-statcard-sub">Por volume de cliques</div>
            </div>
          </header>
          {top.data && top.data.length > 0 ? (
            <TopLinksList links={top.data} />
          ) : (
            <EmptyChart hint="Cadastre seus primeiros links" />
          )}
        </section>

        <section className="fx-statcard">
          <header className="fx-statcard-head">
            <div>
              <div className="fx-statcard-title">Distribuição por tag</div>
              <div className="fx-statcard-sub">Cliques · lifetime</div>
            </div>
          </header>
          {tagBuckets.data && tagBuckets.data.length > 0 ? (
            <TagDistribution buckets={tagBuckets.data} />
          ) : (
            <EmptyChart hint="Crie tags pra ver a distribuição" />
          )}
        </section>
      </div>
    </div>
  )
}

function KpiCard({
  label,
  value,
  valueClass = '',
  delta,
  deltaKind = 'up',
  spark,
}: {
  label: string
  value: string | number
  valueClass?: string
  delta: string
  deltaKind: 'up' | 'down' | 'neutral'
  spark?: number[]
}) {
  return (
    <div className="fx-kpi">
      <div className="fx-kpi-label">{label}</div>
      <div className={'fx-kpi-value ' + valueClass}>{value}</div>
      <div className="fx-kpi-row">
        <span className={'fx-kpi-delta fx-kpi-delta-' + deltaKind}>
          {deltaKind === 'up' ? '▲' : deltaKind === 'down' ? '▼' : '·'} {delta}
        </span>
        {spark && spark.length > 1 && <Sparkline data={spark} width={70} height={22} />}
      </div>
    </div>
  )
}

function Sparkline({ data, width, height }: { data: number[]; width: number; height: number }) {
  const max = Math.max(...data, 1)
  const min = Math.min(...data, 0)
  const step = width / (data.length - 1)
  const path = data
    .map((v, i) => {
      const x = i * step
      const y = height - ((v - min) / (max - min || 1)) * (height - 2) - 1
      return (i === 0 ? 'M' : 'L') + x.toFixed(1) + ',' + y.toFixed(1)
    })
    .join(' ')
  return (
    <svg width={width} height={height} className="fx-spark">
      <path
        d={path}
        fill="none"
        stroke="var(--fx-accent)"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

function AreaChart({ data, width, height }: { data: DailyPoint[]; width: number; height: number }) {
  const pad = { l: 36, r: 12, t: 14, b: 22 }
  const w = width - pad.l - pad.r
  const h = height - pad.t - pad.b
  const series = data.map((d) => d.clicks)
  const max = Math.max(...series, 1) * 1.1
  const step = w / Math.max(series.length - 1, 1)
  const pts = series.map((v, i) => [pad.l + i * step, pad.t + h - (v / max) * h] as [number, number])
  const path = pts.map(([x, y], i) => (i === 0 ? 'M' : 'L') + x.toFixed(1) + ',' + y.toFixed(1)).join(' ')
  const area = path + ` L${pad.l + w},${pad.t + h} L${pad.l},${pad.t + h} Z`
  const avg = series.map((_, i) => {
    const slice = series.slice(Math.max(0, i - 6), i + 1)
    return slice.reduce((a, b) => a + b, 0) / slice.length
  })
  const avgPts = avg.map((v, i) => [pad.l + i * step, pad.t + h - (v / max) * h] as [number, number])
  const avgPath = avgPts
    .map(([x, y], i) => (i === 0 ? 'M' : 'L') + x.toFixed(1) + ',' + y.toFixed(1))
    .join(' ')
  const yTicks = [0, 0.5, 1].map((t) => Math.round(max * t))
  const [hover, setHover] = useState<number | null>(null)

  // Flip the tooltip away from the chart edges so it never clips off the
  // card. Anchored from left/right depending on hover position.
  let tooltipStyle: CSSProperties = { display: 'none' }
  if (hover !== null) {
    const x = pad.l + hover * step
    const y = pad.t + h - (series[hover] / max) * h
    const nearLeft = x < 80
    const nearRight = x > width - 80
    tooltipStyle = {
      position: 'absolute',
      top: y,
      left: nearRight ? undefined : x,
      right: nearRight ? width - x : undefined,
      transform: nearLeft
        ? 'translate(8px, calc(-100% - 12px))'
        : nearRight
          ? 'translate(-8px, calc(-100% - 12px))'
          : 'translate(-50%, calc(-100% - 12px))',
    }
  }

  return (
    <div style={{ position: 'relative', width, height }}>
      <svg width={width} height={height} className="fx-chart">
        <defs>
          <linearGradient id="fx-area-grad" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="var(--fx-accent)" stopOpacity="0.32" />
            <stop offset="100%" stopColor="var(--fx-accent)" stopOpacity="0" />
          </linearGradient>
        </defs>
        {yTicks.map((t, i) => {
          const y = pad.t + h - (t / max) * h
          return (
            <g key={i}>
              <line x1={pad.l} y1={y} x2={pad.l + w} y2={y} stroke="var(--fx-border-2)" strokeDasharray="2 3" />
              <text x={pad.l - 6} y={y + 3} textAnchor="end" className="fx-chart-tick">
                {t}
              </text>
            </g>
          )
        })}
        <path d={area} fill="url(#fx-area-grad)" />
        <path d={path} fill="none" stroke="var(--fx-accent)" strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />
        <path d={avgPath} fill="none" stroke="var(--fx-ink-3)" strokeWidth="1.4" strokeDasharray="3 3" opacity="0.7" />

        {[0, Math.floor(series.length * 0.25), Math.floor(series.length * 0.5), Math.floor(series.length * 0.75), series.length - 1].map((i, idx) => {
          const x = pad.l + i * step
          const days = series.length
          const labels = [`−${days - 1}d`, `−${Math.floor((days - 1) * 0.75)}d`, `−${Math.floor((days - 1) * 0.5)}d`, `−${Math.floor((days - 1) * 0.25)}d`, 'hoje']
          return (
            <text key={idx} x={x} y={height - 6} textAnchor="middle" className="fx-chart-tick">
              {labels[idx]}
            </text>
          )
        })}

        {/* Hover indicator: vertical guide + concentric circle on the point */}
        {hover !== null && (
          <>
            <line
              x1={pad.l + hover * step}
              x2={pad.l + hover * step}
              y1={pad.t}
              y2={pad.t + h}
              stroke="var(--fx-accent)"
              strokeWidth="1"
              strokeDasharray="2 3"
              opacity="0.55"
            />
            <circle
              cx={pad.l + hover * step}
              cy={pad.t + h - (series[hover] / max) * h}
              r="7"
              fill="var(--fx-accent)"
              fillOpacity="0.18"
            />
            <circle
              cx={pad.l + hover * step}
              cy={pad.t + h - (series[hover] / max) * h}
              r="3.5"
              fill="var(--fx-accent)"
              stroke="var(--fx-surface-3)"
              strokeWidth="2"
            />
          </>
        )}

        {/* Transparent hit-zones — one per day. Wider than the visible step
            so the cursor doesn't fall between buckets. */}
        {data.map((_, i) => (
          <rect
            key={i}
            x={pad.l + i * step - step / 2}
            y={pad.t}
            width={step}
            height={h}
            fill="transparent"
            style={{ cursor: 'crosshair' }}
            onMouseEnter={() => setHover(i)}
            onMouseLeave={() => setHover(null)}
          />
        ))}
      </svg>

      {hover !== null && (
        <div className="fx-chart-tooltip" style={tooltipStyle}>
          <div className="fx-chart-tooltip-date">{formatChartDate(data[hover].date)}</div>
          <div className="fx-chart-tooltip-value">
            <strong>{series[hover]}</strong> {series[hover] === 1 ? 'clique' : 'cliques'}
          </div>
        </div>
      )}
    </div>
  )
}

function formatChartDate(iso: string) {
  const d = new Date(iso)
  // "12 mai · qua"
  const day = d.getDate()
  const month = d.toLocaleDateString('pt-BR', { month: 'short' }).replace('.', '')
  const wd = d.toLocaleDateString('pt-BR', { weekday: 'short' }).replace('.', '')
  return `${day} ${month} · ${wd}`
}

function MomCompare({ prev, curr }: { prev: number; curr: number }) {
  const max = Math.max(prev, curr, 1)
  return (
    <div className="fx-mom">
      <div className="fx-mom-bar-wrap">
        <div className="fx-mom-bar-lbl">30d ant.</div>
        <div className="fx-mom-bar fx-mom-bar-prev">
          <div style={{ width: (prev / max) * 100 + '%' }} />
        </div>
        <div className="fx-mom-bar-num">{prev.toLocaleString('pt-BR')}</div>
      </div>
      <div className="fx-mom-bar-wrap">
        <div className="fx-mom-bar-lbl">Atual</div>
        <div className="fx-mom-bar fx-mom-bar-curr">
          <div style={{ width: (curr / max) * 100 + '%' }} />
        </div>
        <div className="fx-mom-bar-num fx-mom-bar-num-accent">{curr.toLocaleString('pt-BR')}</div>
      </div>
      <div className="fx-mom-foot">
        <span>
          <Icon d={I.flame} size={12} /> Δ {(curr - prev).toLocaleString('pt-BR')} cliques
        </span>
      </div>
    </div>
  )
}

function TopLinksList({ links }: { links: TopLink[] }) {
  const maxClicks = Math.max(...links.map((l) => l.clicks), 1)
  return (
    <ol className="fx-toplinks">
      {links.map((l, i) => {
        const delta =
          l.clicks_prev_30d === 0
            ? l.clicks_30d > 0
              ? '+100%'
              : '—'
            : (l.clicks_30d - l.clicks_prev_30d) / l.clicks_prev_30d
        const deltaStr =
          typeof delta === 'string'
            ? delta
            : (delta >= 0 ? '+' : '') + Math.round(delta * 100) + '%'
        const deltaDown = typeof delta === 'number' && delta < 0
        return (
          <li key={l.id} className="fx-toplink">
            <span className="fx-toplink-rank">{String(i + 1).padStart(2, '0')}</span>
            <div className="fx-toplink-text">
              <div className="fx-toplink-title">{l.title}</div>
              <div className="fx-toplink-host">
                {l.host} · /go/{l.id}
              </div>
            </div>
            <div className="fx-toplink-bar">
              <div className="fx-toplink-bar-fill" style={{ width: (l.clicks / maxClicks) * 100 + '%' }} />
            </div>
            <div className="fx-toplink-num">{l.clicks}</div>
            <div
              className={
                'fx-toplink-delta ' + (deltaDown ? 'fx-toplink-delta-down' : 'fx-toplink-delta-up')
              }
            >
              {deltaStr}
            </div>
          </li>
        )
      })}
    </ol>
  )
}

function TagDistribution({
  buckets,
}: {
  buckets: { name: string; color: string; clicks: number; links: number }[]
}) {
  const max = Math.max(...buckets.map((b) => b.clicks), 1)
  return (
    <ul className="fx-tagdist">
      {buckets.map((t) => (
        <li key={t.name} className="fx-tagdist-row">
          <span className="fx-tagdist-dot" style={{ background: t.color }} />
          <span className="fx-tagdist-label">{t.name}</span>
          <div className="fx-tagdist-bar">
            <div
              className="fx-tagdist-bar-fill"
              style={{ width: (t.clicks / max) * 100 + '%', background: t.color }}
            />
          </div>
          <span className="fx-tagdist-num">{t.clicks}</span>
        </li>
      ))}
    </ul>
  )
}

function formatBytes(b: number): string {
  if (b < 1024) return `${b} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let n = b / 1024
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n >= 10 ? 0 : 1)} ${units[i]}`
}

function EmptyChart({ hint }: { hint: string }) {
  return (
    <div
      style={{
        padding: 32,
        textAlign: 'center',
        color: 'var(--fx-ink-4)',
        fontSize: 13,
      }}
    >
      {hint}
    </div>
  )
}
