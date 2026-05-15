# Changelog

## [1.1.0](https://github.com/justoeu/foldex/compare/v1.0.8...v1.1.0) (2026-05-15)


### Features

* **i18n:** fix pluralization + update test assertions to EN ([84d6c0c](https://github.com/justoeu/foldex/commit/84d6c0c0f3ce1ef853e2eb437c855f0f05385ffd))
* **i18n:** foundation — react-i18next + EN/PT/ES + locale picker in topbar ([804af15](https://github.com/justoeu/foldex/commit/804af1590ce9ade7b6ca9d7231b1fb16b4fb224e))
* **i18n:** translate all UI components (Phase 2 — big-bang extraction) ([88a28a2](https://github.com/justoeu/foldex/commit/88a28a241fe53beb5087f4d3f7278911227ec07c))
* **mobile:** LAN access + single-row topbar + dialog reworks ([8c50b9e](https://github.com/justoeu/foldex/commit/8c50b9e28af4c050ece685a00c98e7cd69c02904))
* **mobile:** PWA-grade responsive layout + installable manifest ([386b970](https://github.com/justoeu/foldex/commit/386b97047d7c61ff8c784f8334a8d24a3314fe5d))
* **mobile:** service worker + FAB + overflow menu (PWA complete) ([dde0fec](https://github.com/justoeu/foldex/commit/dde0fec885761f582d2e8b9e0556d4eff82b7818))
* paste a URL anywhere → New Link dialog pre-filled ([26a4947](https://github.com/justoeu/foldex/commit/26a49475d6447cfd0b263dd526530333fb3e99c4))
* **slug:** friendly slugs — `/go/jira-board` (ADR-7 done) ([79b397a](https://github.com/justoeu/foldex/commit/79b397a2f3f03e3a013beeb26a5c6c7bc6d595b2))


### Bug Fixes

* **compose:** mark foldex network external in db + services compose files ([8076085](https://github.com/justoeu/foldex/commit/80760859e1f87d27098801b506cef7d09de80b14))
* **make:** respect POSTGRES_HOST — don't start foldex-db when user has their own ([e7fe0dc](https://github.com/justoeu/foldex/commit/e7fe0dcb7c75332b64f292574c32694e700172a0))
* **pwa:** bust stale SW cache + no-store on shell files ([7bf27a6](https://github.com/justoeu/foldex/commit/7bf27a681529ba44f6d3ef17fe483549fb1cca1b))
* **ui:** graceful fallback when favicon / og:image fail to load ([4495ec5](https://github.com/justoeu/foldex/commit/4495ec5257af1bb484adb5ab8934181c029c425e))
* **ui:** show /go/{slug} instead of /go/{id} in palette + stats ([7921c39](https://github.com/justoeu/foldex/commit/7921c39be5bf78b96ace1acbbf3e192c02faa2ff))
* **ui:** sort segment goes icon-only — frees ~220px on the topbar ([66ce5ed](https://github.com/justoeu/foldex/commit/66ce5ed6581114fafc8e9c1a09097e9872fdaf3b))
* **ui:** topbar grid was overflowing after adding the locale picker ([de94286](https://github.com/justoeu/foldex/commit/de94286b6cc0756f98416890d6b4bc1bf61f8b55))
* **ui:** truncate slug hint in command palette with ellipsis ([21dbe74](https://github.com/justoeu/foldex/commit/21dbe744b731dfaa2a33c0836526b6472b866fba))


### Documentation

* add Home empty-state screenshot to README ([9a37146](https://github.com/justoeu/foldex/commit/9a37146f8aadef5f97053d792f227fa30ae68bfd))
* add README.pt-BR + language switcher in both READMEs ([54ebd84](https://github.com/justoeu/foldex/commit/54ebd845b893604e95250668fdcf429d26b81c73))
* paste-to-create + mobile overhaul + LAN access (§3 sync) ([9a7ae18](https://github.com/justoeu/foldex/commit/9a7ae1899655c20e4f74aec87f43d11333707c2b))
* translate README to English ([7035c83](https://github.com/justoeu/foldex/commit/7035c83a2edf0bff3d62b06d2965d68e8e7794dd))
* use the real Home screenshot as the README hero ([ffd81c4](https://github.com/justoeu/foldex/commit/ffd81c497221501e3f285f4a559273d5c8f500c7))
