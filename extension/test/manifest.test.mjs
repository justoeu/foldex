import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const manifest = JSON.parse(readFileSync(join(ROOT, "manifest.json"), "utf8"));

const PERMISSION_NAMESPACES = {
  tabs: "activeTab",
  storage: "storage",
  scripting: "scripting",
  alarms: "alarms",
};

describe("manifest (SDD R2.1)", () => {
  test("declares exactly the SDD permission set and no broad host access", () => {
    assert.deepEqual([...manifest.permissions].sort(), [
      "activeTab",
      "alarms",
      "scripting",
      "storage",
    ]);
    assert.equal(manifest.host_permissions, undefined);
    assert.deepEqual(manifest.optional_host_permissions, ["http://*/*", "https://*/*"]);
    assert.equal(manifest.manifest_version, 3);
    assert.equal(manifest.name, "foldex");
    assert.match(manifest.version, /^\d+\.\d+\.\d+$/);
    assert.equal(manifest.default_locale, "en");
    assert.equal(manifest.action.default_popup, "popup.html");
  });

  test("maps ⌘⇧S / Ctrl+Shift+S to opening the popup", () => {
    const command = manifest.commands?._execute_action;
    assert.ok(command, "commands._execute_action missing");
    assert.equal(command.suggested_key?.default, "Ctrl+Shift+S");
    assert.equal(command.suggested_key?.mac, "Command+Shift+S");
  });

  test("icons exist as PNGs for every declared size", () => {
    assert.deepEqual(manifest.icons, {
      "16": "icons/fx-16.png",
      "32": "icons/fx-32.png",
      "48": "icons/fx-48.png",
      "128": "icons/fx-128.png",
    });
    for (const file of Object.values(manifest.icons)) {
      const path = join(ROOT, file);
      assert.ok(existsSync(path), `${file} missing`);
      const pngSignature = readFileSync(path).subarray(0, 8);
      assert.deepEqual(
        [...pngSignature],
        [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a],
        `${file} is not a PNG`,
      );
    }
  });

  test("every chrome namespace the modules call is covered by a declared permission", () => {
    const namespaces = new Set();
    for (const name of readdirSync(ROOT).filter((f) => f.endsWith(".js"))) {
      const source = readFileSync(join(ROOT, name), "utf8");
      for (const match of source.matchAll(/chromeApi\.(\w+)/g)) {
        namespaces.add(match[1]);
      }
    }
    // runtime and permissions are permission-free APIs; everything else must
    // be declared — the addon never ships a call it did not ask for.
    const required = new Set(
      [...namespaces]
        .map((ns) => PERMISSION_NAMESPACES[ns])
        .filter(Boolean),
    );
    for (const permission of required) {
      assert.ok(manifest.permissions.includes(permission), `${permission} missing`);
    }
    assert.ok(namespaces.has("scripting"), "scripting should be in use (capture)");
  });
});
