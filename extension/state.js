// Pure reducers for the popup's folder/tag selection. The controller owns
// all DOM and i18n; these stay data-in/data-out.

export function toggleTag(selected, name) {
  return selected.includes(name)
    ? selected.filter((tag) => tag !== name)
    : [...selected, name];
}

export function addTagDraft({ draft, selected, pending }, existingTags) {
  const name = draft.trim();
  if (!name) return { draft, selected, pending };

  const exists = existingTags.some((tag) => tag.name === name) || pending.includes(name);
  return {
    draft: "",
    selected: selected.includes(name) ? selected : [...selected, name],
    pending: exists ? pending : [...pending, name],
  };
}

export function splitTagSelection(selected, existingTags) {
  const tag_ids = [];
  const pending_tags = [];
  for (const name of selected) {
    const existing = existingTags.find((tag) => tag.name === name);
    if (existing) tag_ids.push(existing.id);
    else pending_tags.push(name);
  }
  return { tag_ids, pending_tags };
}

export function selectFolder(currentFolderId, folderId) {
  return currentFolderId === folderId ? null : folderId;
}

// Hint values, not strings: formatting/plurals live behind chrome.i18n in
// the controller.
export function folderHint(selectedFolder) {
  return selectedFolder ? selectedFolder.count ?? 0 : null;
}

export function tagHint(selected) {
  return selected.length;
}
