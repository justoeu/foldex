import { describe, test } from "node:test";
import assert from "node:assert/strict";

import {
  addTagDraft,
  folderHint,
  selectFolder,
  splitTagSelection,
  tagHint,
  toggleTag,
} from "../state.js";

const TAGS = [
  { id: 1, name: "IA" },
  { id: 2, name: "git" },
  { id: 3, name: "3D" },
];

describe("tag chips", () => {
  test("toggleTag adds and removes immutably", () => {
    const selected = ["IA"];
    assert.deepEqual(toggleTag(selected, "git"), ["IA", "git"]);
    assert.deepEqual(toggleTag(selected, "IA"), []);
    assert.deepEqual(selected, ["IA"]);
  });

  test("addTagDraft trims, dedupes against existing and pending chips, and selects", () => {
    const next = addTagDraft(
      { draft: "  ClaudeCode  ", selected: ["IA"], pending: ["projeto"] },
      TAGS,
    );
    assert.deepEqual(next, { draft: "", selected: ["IA", "ClaudeCode"], pending: ["projeto", "ClaudeCode"] });

    const dupeExisting = addTagDraft({ draft: "git", selected: [], pending: [] }, TAGS);
    assert.deepEqual(dupeExisting, { draft: "", selected: ["git"], pending: [] });

    const dupePending = addTagDraft({ draft: "projeto", selected: [], pending: ["projeto"] }, TAGS);
    assert.deepEqual(dupePending, { draft: "", selected: ["projeto"], pending: ["projeto"] });

    assert.deepEqual(addTagDraft({ draft: "   ", selected: ["IA"], pending: [] }, TAGS), {
      draft: "   ",
      selected: ["IA"],
      pending: [],
    });
  });

  test("splitTagSelection sends existing tags as ids and new ones as pending_tags", () => {
    const { tag_ids, pending_tags } = splitTagSelection(["IA", "ClaudeCode", "3D"], TAGS);

    assert.deepEqual(tag_ids, [1, 3]);
    assert.deepEqual(pending_tags, ["ClaudeCode"]);
  });
});

describe("folder selection and hints", () => {
  test("selectFolder is single-select; hints carry the count for the controller to format", () => {
    assert.equal(selectFolder(null, 12), 12);
    assert.equal(selectFolder(12, 12), null);
    assert.equal(selectFolder(3, 12), 12);

    assert.equal(folderHint(null), null);
    assert.equal(folderHint({ id: 4, name: "Ideias", count: 1 }), 1);

    assert.equal(tagHint([]), 0);
    assert.equal(tagHint(["IA", "git"]), 2);
  });
});
