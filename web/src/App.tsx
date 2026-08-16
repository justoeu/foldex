import './styles/foldex.css'
import './styles/overrides.css'

import { useEntries, flattenEntries, type EntryListParams } from './api/entries'
import { useFolders, type FolderListParams } from './api/folders'
import type { Folder } from './api/types'
import { useAppWorkspaceController } from './AppWorkspace'
import { useAppDialogController } from './AppDialogs'
import { useAppDndController } from './hooks/useAppDndController'
import { useAppNavigationController, useFolderLockRecovery } from './AppNavigation'
import { AppShell, type AppContentState } from './AppShell'
import { MutationAlert } from './components/MutationAlert'
import { useEntryCounts } from './hooks/useEntryCounts'

function entryParams(
  q: string,
  tagIds: number[],
  sort: EntryListParams['sort'],
  folderId: number | null,
  unlockToken: string | undefined,
): EntryListParams {
  const common = { q, tagIds, sort, unlockToken }
  return folderId === null ? { ...common, ungrouped: true } : { ...common, folderId }
}

function folderParams(folderId: number | null, unlockToken: string | undefined): FolderListParams {
  return { scope: folderId === null ? 'root' : folderId, unlockToken }
}

function foldersOrEmpty(folders: Folder[] | undefined): Folder[] {
  return folders ?? []
}

export default function App() {
  const workspace = useAppWorkspaceController()
  const allFoldersQuery = useFolders({ scope: null, fields: 'minimal' })
  const navigation = useAppNavigationController(allFoldersQuery.data)
  const dialogs = useAppDialogController(allFoldersQuery.data)
  const entriesQuery = useEntries(entryParams(
    workspace.q,
    workspace.selectedTags,
    workspace.sort,
    navigation.openFolder,
    navigation.currentUnlockToken,
  ))
  const entryCountsQuery = useEntryCounts()
  const foldersQuery = useFolders(folderParams(navigation.openFolder, navigation.currentUnlockToken))

  useFolderLockRecovery({
    entriesError: entriesQuery.error,
    foldersError: foldersQuery.error,
    navigation,
  })

  const dnd = useAppDndController({
    folders: allFoldersQuery.data,
    openFolder: navigation.openFolder,
    onFolderCreated: dialogs.openCreatedFolder,
  })
  const entries = flattenEntries(entriesQuery.data)
  const content: AppContentState = {
    entries,
    folders: foldersOrEmpty(foldersQuery.data),
    allFolders: foldersOrEmpty(allFoldersQuery.data),
    entriesLoading: entriesQuery.isLoading,
    entriesFetching: entriesQuery.isFetching,
    foldersFetching: foldersQuery.isFetching,
    allFoldersFetching: allFoldersQuery.isFetching,
    hasMoreEntries: entriesQuery.hasNextPage === true,
    fetchingMoreEntries: entriesQuery.isFetchingNextPage,
    refetchEntries: entriesQuery.refetch,
    refetchFolders: foldersQuery.refetch,
    refetchAllFolders: allFoldersQuery.refetch,
    fetchMoreEntries: entriesQuery.fetchNextPage,
  }

  return (
    <>
      <AppShell
        workspace={workspace}
        navigation={navigation}
        dialogs={dialogs}
        dnd={dnd}
        content={content}
        totalLinks={entryCountsQuery.data?.links ?? 0}
      />
      {dnd.mergeError && <MutationAlert message={dnd.mergeError} onClose={dnd.dismissMergeError} />}
    </>
  )
}
