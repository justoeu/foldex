import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = dirname(fileURLToPath(import.meta.url));
const LOCALES = ["en", "pt", "es"];

function messagesOf(locale) {
  return JSON.parse(
    readFileSync(join(ROOT, "_locales", locale, "messages.json"), "utf8"),
  );
}

describe("extension i18n parity", () => {
  test("manifest declares default_locale en", async () => {
    const manifest = await Bun.file(join(ROOT, "manifest.json")).json();
    expect(manifest.default_locale).toBe("en");
  });

  test("en, pt and es ship identical message keys", () => {
    const locales = readdirSync(join(ROOT, "_locales")).sort();
    expect(locales).toEqual(LOCALES.slice().sort());
    const keys = LOCALES.map((locale) =>
      Object.keys(messagesOf(locale)).sort(),
    );
    expect(keys[1]).toEqual(keys[0]);
    expect(keys[2]).toEqual(keys[0]);
    expect(keys[0].length).toBeGreaterThan(0);
  });

  test("popup and options JS go through chrome.i18n, not English literals", async () => {
    const sources = await Promise.all(
      ["popup.js", "options.js"].map((name) =>
        Bun.file(join(ROOT, name)).text(),
      ),
    );
    const joined = sources.join("\n");
    expect(joined).toMatch(/i18n\.getMessage|t\(chromeApi/);
    for (const banned of [
      "URL is required",
      "Saving…",
      "Saved ✓",
      "Requesting access…",
      "Saved.",
      "not signed in — set an API token in settings",
    ]) {
      expect(joined).not.toContain(banned);
    }
  });

  test("popup and options HTML use __MSG_ substitutions", async () => {
    for (const name of ["popup.html", "options.html"]) {
      const html = await Bun.file(join(ROOT, name)).text();
      expect(html).toMatch(/__MSG_/);
    }
  });
});
