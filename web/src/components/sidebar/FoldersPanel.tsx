import { Fragment, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'
import type { Folder } from '../../api/types'

type Props = Readonly<{
  // Flat list of every folder (any depth) — the same `useFolders({ scope: null,
  // fields: 'minimal' })` query App.tsx already feeds the breadcrumb resolver.
  folders: Folder[]
  // Folder currently open in the main area (navigation.openFolder); the
  // matching row renders active.
  openFolder: number | null
  // Gated open (AppNavigation.requestOpenFolder — ADR-28). Protected folders
  // never render here, so the gate is a safety net, not the filter.
  onOpenFolder: (id: number) => void
  onNewFolder: () => void
}>

type Node = { folder: Folder; children: Node[] }

// Builds the visibility tree. A password-protected folder (ADR-28) drops out
// together with its whole subtree: a child of a hidden folder would navigate
// into a scope the sidebar claims does not exist, so it stays hidden too.
// An orphan (parent_id pointing at a folder the API did not return) renders
// at the root rather than vanishing.
function buildVisibleTree(folders: Folder[]): Node[] {
  const byId = new Map(folders.map((f) => [f.id, f]))
  const childrenOf = new Map<number, Folder[]>()
  const roots: Folder[] = []
  for (const f of folders) {
    if (f.parent_id == null || !byId.has(f.parent_id)) roots.push(f)
    else childrenOf.set(f.parent_id, [...(childrenOf.get(f.parent_id) ?? []), f])
  }
  const toNodes = (list: Folder[]): Node[] =>
    list
      .filter((f) => !f.has_password)
      .map((f) => ({ folder: f, children: toNodes(childrenOf.get(f.id) ?? []) }))
  return toNodes(roots)
}

export function FoldersPanel({ folders, openFolder, onOpenFolder, onNewFolder }: Props) {
  const { t } = useTranslation()
  const tree = buildVisibleTree(folders)

  const renderNodes = (nodes: Node[], child: boolean): ReactNode =>
    nodes.map((node) => (
      <Fragment key={node.folder.id}>
        <button
          type="button"
          className={
            'fx-side-row' +
            (child ? ' fx-folder-child-row' : '') +
            (openFolder === node.folder.id ? ' fx-side-row-active' : '')
          }
          onClick={() => onOpenFolder(node.folder.id)}
        >
          <span className="fx-side-dot" style={{ background: node.folder.color }} />
          <span className="fx-side-label">{node.folder.name}</span>
          <span className="fx-side-count">{node.folder.link_count}</span>
        </button>
        {node.children.length > 0 && (
          <div className="fx-folder-children">{renderNodes(node.children, true)}</div>
        )}
      </Fragment>
    ))

  return (
    <>
      {tree.length === 0 ? (
        <div className="fx-side-empty">{t('sidebar.empty_folders')}</div>
      ) : (
        renderNodes(tree, false)
      )}
      <div className="fx-side-actions">
        <button
          className="fx-side-action"
          onClick={onNewFolder}
          data-tooltip={t('sidebar.new_folder_tooltip')}
        >
          <Icon d={I.plus} size={13} />
          <span>{t('sidebar.new_folder')}</span>
        </button>
      </div>
    </>
  )
}
