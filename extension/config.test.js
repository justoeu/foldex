import { describe, expect, test } from "bun:test";

import {
  DEFAULT_CONFIG,
  normalizeBaseUrl,
  permissionForBaseUrl,
} from "./config.js";

// The Bearer token rides every extension call. Over plain http to a
// non-loopback host it is cleartext on the wire for anyone on-path (ARP
// spoof, hostile wifi, corporate middlebox) — the same combination the
// backend itself refuses (INV-093). Loopback http and any https origin stay
// allowed.
describe("normalizeBaseUrl rejects cleartext non-loopback origins", () => {
  test("rejects http on a LAN host", () => {
    expect(() => normalizeBaseUrl("http://192.168.1.20:9089")).toThrow(
      /loopback/i,
    );
  });

  test("rejects http on a remote hostname", () => {
    expect(() => normalizeBaseUrl("http://nas.lan:9089")).toThrow(/loopback/i);
    expect(() => normalizeBaseUrl("http://foldex.example.com")).toThrow(
      /loopback/i,
    );
  });

  test("allows the shipped default (loopback http)", () => {
    expect(normalizeBaseUrl(DEFAULT_CONFIG.baseUrl)).toBe(
      "http://localhost:9089",
    );
  });

  test("allows loopback http variants", () => {
    expect(normalizeBaseUrl("http://127.0.0.1:9089")).toBe(
      "http://127.0.0.1:9089",
    );
    expect(normalizeBaseUrl("http://[::1]:9089")).toBe("http://[::1]:9089");
  });

  test("allows https on any host", () => {
    expect(normalizeBaseUrl("https://nas.lan:9444")).toBe("https://nas.lan:9444");
    expect(normalizeBaseUrl("https://foldex.example.com")).toBe(
      "https://foldex.example.com",
    );
  });

  test("empty input still falls back to the safe default", () => {
    expect(normalizeBaseUrl("  ")).toBe("http://localhost:9089");
  });

  // Ported from permissions.test.js (superseded by the manifest/node suites)
  // when the popup/options rework landed — same coverage, same module.
  test("normalizes the backend URL while retaining a reverse-proxy path", () => {
    expect(
      normalizeBaseUrl(" HTTPS://Foldex.Example:443/app///?ignored=1#ignored "),
    ).toBe("https://foldex.example/app");
    expect(normalizeBaseUrl("http://localhost:9089/")).toBe(
      "http://localhost:9089",
    );
    expect(permissionForBaseUrl("https://foldex.example/app")).toEqual({
      origins: ["https://foldex.example/*"],
    });
    expect(permissionForBaseUrl("http://localhost:9089")).toEqual({
      origins: ["http://localhost:9089/*"],
    });
    expect(() => normalizeBaseUrl("ftp://foldex.example")).toThrow(
      "HTTP or HTTPS",
    );
    expect(() =>
      normalizeBaseUrl("https://user:secret@foldex.example"),
    ).toThrow("credentials");
  });
});
