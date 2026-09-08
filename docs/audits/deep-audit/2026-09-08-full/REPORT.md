# Deep Audit — CSRF/SSRF OWASP + Atlas + Quality Guild

**Projeto:** `foldex` · **versão** `2.21.2` (HEAD `429c7ba`) · **branch** `main`
**Rodada:** 2026-09-08 · modo `full` · lentes: CSRF/SSRF (A01/A10) · Atlas · Dédalo · Eco · Lacônio · Atena

HTML: `docs/audits/deep-audit/2026-09-08-full/report.html` (cópia `docs/audits/deep-audit-report.html`)

## CSRF / SSRF (OWASP)

0 achados abertos. 6 refutados (SEC-CER-001…006):

| Lente | OWASP | Guarda |
|---|---|---|
| CSRF | A01:2021 · CWE-352 · V4.2.2 · WSTG-SESS-05 | `X-Foldex-CSRF` vs hash da sessão; SameSite=Lax/Strict; CORS allowlist |
| SSRF | A10:2021 · CWE-918 · V5.2.6 · WSTG-INPV-19 | `outboundhttp.safeDialer` resolve→recusa IMDS/RFC1918→dial no IP |

## Corrigido nesta sessão

- DUP-ECO-002 — hero/admin 2FA usam `hasSecondFactor` (email-only conta)
- DUP-ECO-003 — `ValidateAbsoluteHTTPURL` é o predicado único (scheme case-insensitive + Host)
- DUP-ECO-001 — teto bcrypt 72 bytes em master + folder (`pwhash.MaxPlainBytes`)
- ARCH-ATL-002 — `pkg/crudupdate` não importa `tags`; `tags.ApplyPatchTags`
- ARCH-ATL-003 — backup sanitiza via `htmlsanitize.SanitizeAndPlain`
- ARCH-ATL-004 — `Sort`/`ViewMode` em `web/src/lib/viewPrefs.ts`

## Ainda aberto (HTML)

- ARCH-ATL-001 HIGH — `auth` god package (split admin/audit/2fa: PR próprio)
- CPX-DED-001…003 HIGH — `userObjectKeys`, restore parent BFS, `updateOnce`
- MEDIUM/LOW do Guild (Dédalo restante, Eco hostname/upload/hint, Atena, Lacônio)
