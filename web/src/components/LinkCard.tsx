import { memo } from 'react'
import { useTranslation } from 'react-i18next'
import { safeImageUrl } from '../lib/url'
import type { Link, MergeSource } from '../api/types'
import { hasUnseenChange, useLinkCardInteractions } from './LinkCardInteractions'
import { LinkCardBadges, LinkCardBody, LinkCardPreview } from './LinkCardParts'

type Props = {
  link: Link
  onEdit: (link: Link) => void
  density?: 'normal' | 'short' | 'medium' | 'tall'
  onMergeWith?: (source: MergeSource, targetId: number) => void
  onDelete: (link: Link) => void
  onPin: (link: Link, pinned: boolean) => void
  onRefreshPreview: (id: number) => void
  onMarkSeen: (id: number) => void
}

function densityFor(link: Link, imageVisible: boolean): 'tall' | 'medium' | 'short' {
  if (link.og_image_url && imageVisible) return 'tall'
  if (link.description) return 'medium'
  return 'short'
}

function cardClass(
  link: Link,
  density: 'tall' | 'medium' | 'short',
  unseenChange: boolean,
  dragging: boolean,
  dragOver: boolean,
): string {
  return 'fx-card fx-card-' + density +
    (link.pinned ? ' fx-card-pinned' : '') +
    (unseenChange ? ' fx-card-update-alert' : '') +
    (dragging ? ' fx-card-dragging' : '') +
    (dragOver ? ' fx-card-drop-over' : '')
}

export const LinkCard = memo(LinkCardImpl)
LinkCard.displayName = 'LinkCard'

function LinkCardImpl(props: Props) {
  const { link, onEdit, onMergeWith, onDelete, onPin, onRefreshPreview, onMarkSeen } = props
  const { t } = useTranslation()
  const previewSrc = safeImageUrl(link.og_image_url)
  const interaction = useLinkCardInteractions({
    linkId: link.id,
    previewUrl: link.og_image_url,
    onMergeWith,
  })
  const showPreview = !!previewSrc && !interaction.previewErrored
  const unseenChange = hasUnseenChange(link)
  const actions = { onEdit, onDelete, onPin, onRefreshPreview, onMarkSeen }

  return (
    <article
      className={cardClass(
        link,
        densityFor(link, showPreview),
        unseenChange,
        interaction.dragging,
        interaction.dragOver,
      )}
      draggable
      onDragStart={interaction.onDragStart}
      onDragEnd={interaction.onDragEnd}
      onDragOver={interaction.onDragOver}
      onDragEnter={interaction.onDragEnter}
      onDragLeave={interaction.onDragLeave}
      onDrop={interaction.onDrop}
    >
      <LinkCardBadges
        link={link}
        unseenChange={unseenChange}
        actions={actions}
        t={t}
      />
      <LinkCardPreview
        link={link}
        previewSrc={showPreview ? previewSrc : undefined}
        onGo={interaction.onGo}
        onError={interaction.onPreviewError}
      />
      <LinkCardBody
        link={link}
        showPreview={showPreview}
        actions={actions}
        onGo={interaction.onGo}
        t={t}
      />
    </article>
  )
}
