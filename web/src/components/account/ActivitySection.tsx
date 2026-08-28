import { useInfiniteQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { fetchOwnActivity, type AuditEntry } from '../../api/admin'
import { actionLabel } from '../../lib/auditLabels'

/** One page of the feed. The server clamps this too. */
const PAGE_SIZE = 50

/**
 * The account's own activity — the other half of ADR-46's read split.
 *
 * This is the ONLY projection that returns the content label: the caller is the
 * row's actor, so the title of the link they edited is their own. The
 * administrative trail withholds it from everyone, in SQL, which is what lets
 * one table serve both readers without INV-045 depending on which component
 * happens to render it.
 *
 * It lives on the account page rather than in a screen of its own because
 * INV-146 puts everything a user manages about THEMSELVES in one place.
 */
export function ActivitySection() {
  const { t } = useTranslation()

  // useInfiniteQuery rather than a cursor in state plus an accumulating array:
  // the accumulation, the deduplication and "is there another page" are exactly
  // what it does, and the hand-rolled version had to append from inside the
  // fetcher — a side effect in a function React Query is free to re-run on
  // focus, which is why it needed a dedupe set to stay correct.
  const query = useInfiniteQuery({
    queryKey: ['activity'],
    queryFn: ({ pageParam }) => fetchOwnActivity(pageParam),
    initialPageParam: undefined as number | undefined,
    // The keyset cursor is the last id on the page. A short page is the end of
    // the feed — asking again would return the same nothing.
    getNextPageParam: (last: AuditEntry[]) =>
      last.length < PAGE_SIZE ? undefined : last[last.length - 1]?.id,
  })

  const entries = query.data?.pages.flat() ?? []

  return (
    <div className="fx-acc-activity">
      <p className="fx-acc-activity-lede">{t('admin.activity_lede')}</p>

      {query.isPending && <div className="fx-empty">{t('common.loading')}</div>}
      {query.isError && <div className="fx-empty">{t('admin.activity_unavailable')}</div>}
      {!query.isPending && !query.isError && entries.length === 0 && (
        <div className="fx-empty">{t('admin.activity_empty')}</div>
      )}

      {entries.length > 0 && (
        <ul className="fx-acc-activity-list">
          {entries.map((e) => (
            <li className="fx-acc-activity-row" key={e.id} data-testid="fx-activity-row">
              <time className="fx-acc-activity-when" dateTime={e.created_at}>
                {new Date(e.created_at).toLocaleString()}
              </time>
              <span className="fx-acc-activity-action">{actionLabel(t, e.action)}</span>
              {/* Rendered as TEXT. The subject is a title the user typed, and
                  the one thing that must never happen to it is being parsed as
                  markup — note bodies have a sanitizer for that reason, and a
                  label has no business carrying any. */}
              <span className="fx-acc-activity-subject">
                {e.subject || t('admin.audit_detail_absent')}
              </span>
              <span className="fx-acc-activity-ip">{e.ip ?? ''}</span>
            </li>
          ))}
        </ul>
      )}

      {query.hasNextPage && (
        <button
          type="button"
          className="fx-pillbtn"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {t('admin.activity_more')}
        </button>
      )}
    </div>
  )
}
