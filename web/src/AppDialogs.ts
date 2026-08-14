import { useCallback, useState } from 'react'
import { useHotkeys } from 'react-hotkeys-hook'
import { usePasteUrl } from './hooks/usePasteUrl'
import type { Folder, Link } from './api/types'

export function useAppDialogController(allFolders: Folder[] | undefined) {
  const [linkDialogOpen, setLinkDialogOpen] = useState(false)
  const [editLink, setEditLink] = useState<Link | null>(null)
  const [pastedUrl, setPastedUrl] = useState<string | undefined>()
  const [folderDialogOpen, setFolderDialogOpen] = useState(false)
  const [editFolder, setEditFolder] = useState<Folder | null>(null)
  const [folderJustCreated, setFolderJustCreated] = useState(false)
  const [noteDialogOpen, setNoteDialogOpen] = useState(false)
  const [editNoteId, setEditNoteId] = useState<number | null>(null)

  const openLink = useCallback((initialUrl?: string) => {
    setEditLink(null)
    setPastedUrl(initialUrl)
    setLinkDialogOpen(true)
  }, [])
  const openNewLink = useCallback(() => openLink(), [openLink])
  const openPastedLink = useCallback((url: string) => openLink(url), [openLink])
  const openEditLink = useCallback((link: Link) => {
    setEditLink(link)
    setPastedUrl(undefined)
    setLinkDialogOpen(true)
  }, [])
  const closeLink = useCallback(() => {
    setLinkDialogOpen(false)
    setPastedUrl(undefined)
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
    openNewLink,
    openEditLink,
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
