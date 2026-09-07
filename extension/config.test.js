import { describe, expect, test } from "bun:test";

import { DEFAULT_CONFIG, normalizeBaseUrl } from "./config.js";

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
});
