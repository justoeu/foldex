import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import {
  actorOf, blockable, dayColumns, dayLabel, delta, deltaPercent, distributionWidth,
  PINNED_AUDIT_ACTIONS, severityClass, timelineActionChips,
} from './auditFormat'
import type { AuditEntry } from '../../api/admin'

// The browser tsconfig has no @types/node on purpose. This file is the only
// consumer of Node's TZ pin: UTC-midnight buckets must not shift a civil day
// west of Greenwich (INV-180).
declare const process: { env: { TZ?: string } }

describe('delta', () => {
  // The tone is the DIRECTION OF CONCERN, not the sign. Colouring by sign
  // alone paints a drop in failed sign-ins red, which is the opposite of what
  // it means.
  it('calls more failures bad and fewer failures good', () => {
    expect(delta(16, 11, 'up')).toEqual({ label: '+5', tone: 'bad' })
    expect(delta(11, 16, 'up')).toEqual({ label: '-5', tone: 'good' })
  })

  it('leaves a neutral metric neutral in both directions', () => {
    expect(delta(89, 79, 'never').tone).toBe('neutral')
    expect(delta(79, 89, 'never').tone).toBe('neutral')
  })

  it('reports no change without a sign', () => {
    expect(delta(5, 5, 'up')).toEqual({ label: '—', tone: 'neutral' })
  })
})

describe('deltaPercent', () => {
  it('is a rate when there is a previous window to compare against', () => {
    expect(deltaPercent(89, 79, 'never').label).toBe('+13%')
    expect(deltaPercent(70, 79, 'never').label).toBe('-11%')
  })

  // "+∞%" is not information. A window that starts from nothing falls back to
  // the absolute number, which is.
  it('falls back to the count when the previous window was empty', () => {
    expect(deltaPercent(12, 0, 'never').label).toBe('+12')
  })
})

describe('dayColumns', () => {
  const days = [
    { day: '2026-08-26T00:00:00Z', logins: 0, failed: 0, admin: 0, content: 0 },
    { day: '2026-08-27T00:00:00Z', logins: 8, failed: 2, admin: 0, content: 0 },
  ]

  it('scales every series against the tallest day', () => {
    const [empty, busy] = dayColumns(days, 'en')
    expect(empty.total).toBe(0)
    expect(busy.logins).toBe(80)
    expect(busy.admin).toBe(0)
  })

  // A bar that is there and invisible is worse than one that is absent: the
  // axis label below it says something happened.
  it('never rounds a real value down to nothing', () => {
    const [, tiny] = dayColumns(
      [
        { day: '2026-08-26T00:00:00Z', logins: 500, failed: 0, admin: 0, content: 0 },
        { day: '2026-08-27T00:00:00Z', logins: 0, failed: 1, admin: 0, content: 0 },
      ],
      'en',
    )
    expect(tiny.failed).toBeGreaterThan(0)
  })

  // A quiet day still needs a column, or a quiet week renders as a busy one.
  it('keeps empty days as columns', () => {
    expect(dayColumns(days, 'en')).toHaveLength(2)
  })

  // The tooltip reads the raw counts from the column rather than looking the
  // day back up: the lookup would need a fallback for a row that cannot be
  // missing, and an unreachable fallback reads as a case somebody considered.
  it('carries the raw counts alongside the scaled heights', () => {
    const [, busy] = dayColumns(days, 'en')
    expect(busy.counts).toEqual({ logins: 8, failed: 2, admin: 0, content: 0 })
  })

  it('survives a day with no events at all without dividing by zero', () => {
    const [only] = dayColumns([{ day: '2026-08-27T00:00:00Z', logins: 0, failed: 0, admin: 0, content: 0 }], 'en')
    expect(only.logins).toBe(0)
    expect(only.total).toBe(0)
  })
})

describe('dayLabel', () => {
  // The defect is invisible on UTC: midnight UTC is that civil date. Pin a
  // west-of-Greenwich zone so 2026-08-27T00:00:00Z localizes as the 26th.
  const prevTZ = process.env.TZ
  beforeAll(() => { process.env.TZ = 'America/Sao_Paulo' })
  afterAll(() => {
    if (prevTZ === undefined) delete process.env.TZ
    else process.env.TZ = prevTZ
  })

  const civil = (locale: string) =>
    new Date(2026, 7, 27).toLocaleDateString(locale, { day: '2-digit', month: '2-digit' })

  // INV-180's client echo: the bucket is a civil date. Parsing the UTC
  // midnight instant then localizing shifts the day west of Greenwich.
  it('does not shift a UTC-midnight bucket one calendar day west', () => {
    const iso = '2026-08-27T00:00:00Z'
    expect(new Date(iso).toLocaleDateString('en-GB', { day: '2-digit', month: '2-digit' })).toBe('26/08')
    expect(dayLabel(iso, 'en-GB')).toBe(civil('en-GB'))
    expect(dayLabel(iso, 'en-GB')).toBe('27/08')
  })

  it('treats a YYYY-MM-DD prefix as the same civil date', () => {
    expect(dayLabel('2026-08-27', 'pt-BR')).toBe(civil('pt-BR'))
  })

  it('labels the chart column with that civil date', () => {
    const [col] = dayColumns(
      [{ day: '2026-08-27T00:00:00Z', logins: 1, failed: 0, admin: 0, content: 0 }],
      'en-GB',
    )
    expect(col.label).toBe('27/08')
  })
})

describe('distributionWidth', () => {
  it('is a share of the largest slice', () => {
    expect(distributionWidth(8, 16)).toBe(50)
    expect(distributionWidth(16, 16)).toBe(100)
  })
  it('keeps a real slice visible', () => {
    expect(distributionWidth(1, 900)).toBeGreaterThan(0)
  })
  it('does not divide by zero', () => {
    expect(distributionWidth(0, 0)).toBe(0)
  })
})

describe('actorOf', () => {
  const base = { id: 1, action: 'x', category: 'identity', severity: 'info',
    target_email: null, detail: null, ip: null, ip_trusted: false,
    user_agent: null, created_at: '' } as unknown as AuditEntry

  it('names an identity actor by e-mail', () => {
    expect(actorOf({ ...base, actor_email: 'a@b.test', actor_ref: 1 }))
      .toEqual({ kind: 'email', email: 'a@b.test' })
  })

  // A content row has no e-mail BY CONSTRUCTION — the server withheld it —
  // and the opaque reference is what identifies the account instead.
  it('falls back to the opaque reference when the name was withheld', () => {
    expect(actorOf({ ...base, actor_email: null, actor_ref: 7 }))
      .toEqual({ kind: 'ref', ref: 7 })
  })

  // A failed sign-in has no account at all: nobody was authenticated. Rendering
  // "user #0" there would invent an account that does not exist.
  it('reports no actor for an unauthenticated event', () => {
    expect(actorOf({ ...base, actor_email: null, actor_ref: null })).toEqual({ kind: 'none' })
  })
})

describe('timelineActionChips', () => {
  it('pins the identity events ahead of the top-N distribution', () => {
    const chips = timelineActionChips([
      { action: 'link.updated', category: 'content', count: 40 },
      { action: 'login.failed', category: 'identity', count: 2 },
      { action: 'tag.created', category: 'content', count: 3 },
    ])
    expect(chips.map((c) => c.action)).toEqual([
      ...PINNED_AUDIT_ACTIONS,
      'link.updated',
      'tag.created',
    ])
    /* A pinned action with no events still gets a chip reading zero, and that
       zero is the answer: "did a copy of the instance leave the box?" is not a
       question an absent chip should make the reader assume. */
    expect(chips.find((c) => c.action === 'backup.downloaded')?.count).toBe(0)
    expect(chips.find((c) => c.action === 'login.failed')?.count).toBe(2)
  })

  it('caps extras so a busy distribution cannot flood the chip row', () => {
    const pinned = PINNED_AUDIT_ACTIONS.length
    const extras = Array.from({ length: 12 }, (_, i) => ({
      action: `link.extra_${i}`,
      category: 'content' as const,
      count: 10 - (i % 9),
    }))
    const chips = timelineActionChips(extras)
    expect(chips).toHaveLength(pinned + 6)
    expect(chips.slice(pinned).map((c) => c.action)).toEqual(extras.slice(0, 6).map((d) => d.action))
  })
})

describe('blockable', () => {
  // Loopback is how a local operator administers the instance, and the whole
  // access path when AUTH_ENABLED=0. It is also the ONE rail a browser can
  // evaluate: the self-address and trusted-proxy rails need what only the
  // server knows, and they refuse from there with their own codes.
  it('refuses loopback', () => {
    for (const ip of ['127.0.0.1', '127.0.0.53', '::1', '0.0.0.0', '::']) {
      expect(blockable(ip)).toBe(false)
    }
  })

  it('offers an ordinary address', () => {
    expect(blockable('203.0.113.9')).toBe(true)
  })

  it('refuses a row with no address', () => {
    expect(blockable(null)).toBe(false)
  })
})

describe('severityClass', () => {
  it('carries the level so the badge can be styled per level', () => {
    expect(severityClass('critical')).toContain('fx-aud-sev-critical')
    expect(severityClass('info')).toContain('fx-aud-sev-info')
  })
})
