import { useCallback, useState } from 'react'
import { useHotkeys } from 'react-hotkeys-hook'
import { usePasteUrl } from './hooks/usePasteUrl'
import type { Folder, Link } from './api/types'

export function useAppDialogController(allFolders: Folder[] | undefined) {
  const [linkDialogOpen, setLinkDialogOpen] = useState(false)
  const [editLink, setEditLink] = useState<Link | null>(null)
  // Which field the link dialog should hand focus to. It is an INTENT set by
  // the caller, not a property of the link: the same row opens on the URL from
  // the pencil and on the image panel from the card's image button, and the
  // dialog cannot tell those apart on its own.
  const [linkFocus, setLinkFocus] = useState<'url' | 'image'>('url')
  const [pastedUrl, setPastedUrl] = useState<string | undefined>()
  const [folderDialogOpen, setFolderDialogOpen] = useState(false)
  const [editFolder, setEditFolder] = useState<Folder | null>(null)
  const [folderJustCreated, setFolderJustCreated] = useState(false)
  const [noteDialogOpen, setNoteDialogOpen] = useState(false)
  const [editNoteId, setEditNoteId] = useState<number | null>(null)

  const openLink = useCallback((initialUrl?: string) => {
    setEditLink(null)
    setPastedUrl(initialUrl)
    setLinkFocus('url')
    setLinkDialogOpen(true)
  }, [])
  const openNewLink = useCallback(() => openLink(), [openLink])
  const openPastedLink = useCallback((url: string) => openLink(url), [openLink])
  // One opener, two landing points. The two entry points differ ONLY in where
  // focus lands, and keeping them as separate copies meant the next piece of
  // transient state added to one would have to be remembered in the other.
  const openLinkFor = useCallback((link: Link, focus: 'url' | 'image') => {
    setEditLink(link)
    setPastedUrl(undefined)
    setLinkFocus(focus)
    setLinkDialogOpen(true)
  }, [])
  const openEditLink = useCallback((link: Link) => openLinkFor(link, 'url'), [openLinkFor])
  // A card with no preview sends the reader straight to the panel that can
  // give it one.
  const openLinkImage = useCallback((link: Link) => openLinkFor(link, 'image'), [openLinkFor])
  const closeLink = useCallback(() => {
    setLinkDialogOpen(false)
    setPastedUrl(undefined)
    // Reset, or the next Alt+N inherits the image intent from whatever card
    // was opened last and lands on an upload zone for a link with no id yet.
    setLinkFocus('url')
  }, [])

  const openNewFolder = useCallback(() => {
    setEditFolder(null)
    setFolderJustCreated(false)
    setFolderDialogOpen(true)
  }, [])
  const openEditFolder = useCallback((folder: Folder) => {
    setEditFolder(folder)
    setFolderJustCreated(false)
    setFolderDialogOpen(true)
  }, [])
  const openCreatedFolder = useCallback((folder: Folder) => {
    setEditFolder(folder)
    setFolderJustCreated(true)
    setFolderDialogOpen(true)
  }, [])
  const openEditFolderById = useCallback((id: number) => {
    const folder = allFolders?.find((candidate) => candidate.id === id)
    if (folder) openEditFolder(folder)
  }, [allFolders, openEditFolder])
  const closeFolder = useCallback(() => {
    setFolderDialogOpen(false)
    setEditFolder(null)
    setFolderJustCreated(false)
  }, [])

  const openNewNote = useCallback(() => {
    setEditNoteId(null)
    setNoteDialogOpen(true)
  }, [])
  const openEditNote = useCallback((id: number) => {
    setEditNoteId(id)
    setNoteDialogOpen(true)
  }, [])
  const closeNote = useCallback(() => {
    setNoteDialogOpen(false)
    setEditNoteId(null)
  }, [])

  usePasteUrl(openPastedLink)
  useHotkeys('alt+n', (event) => {
    event.preventDefault()
    openNewLink()
  })
  useHotkeys('alt+f', (event) => {
    event.preventDefault()
    openNewFolder()
  })
  useHotkeys('alt+m', (event) => {
    event.preventDefault()
    openNewNote()
  })

  return {
    linkDialogOpen,
    editLink,
    pastedUrl,
    linkFocus,
    openNewLink,
    openEditLink,
    openLinkImage,
    closeLink,
    folderDialogOpen,
    editFolder,
    folderJustCreated,
    openNewFolder,
    openEditFolder,
    openCreatedFolder,
    openEditFolderById,
    closeFolder,
    noteDialogOpen,
    editNoteId,
    openNewNote,
    openEditNote,
    closeNote,
  }
}

export type AppDialogController = ReturnType<typeof useAppDialogController>
