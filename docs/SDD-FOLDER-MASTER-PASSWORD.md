# SDD — Master password (recuperação de senha de pastas) + palavra-dica

> Design de detalhe para o ADR-29. Complementa o ADR-28 (senha por pasta), que
> este documento assume conhecido. **WHAT** e o resumo de decisão vivem no
> ADR-29 (`docs/ARCHITECTURE.md`); aqui está o **HOW** de detalhe.

## 1. Problema

O ADR-28 deixou a recuperação de uma senha de pasta esquecida como "edite o
banco direto" (sem bypass de admin, por design). Para o dono single-user isso é
hostil. Além disso, não havia como registrar uma **dica** que ajudasse a lembrar
a senha. Duas necessidades:

1. **Senha master** — uma senha de recuperação, configurável numa área de
   **Configurações**, capaz de **limpar** a senha de uma pasta esquecida para
   que uma nova possa ser definida.
2. **Palavra-dica por pasta** — uma frase-lembrete (que **não pode** ser igual à
   senha), exibida no popup de unlock.

## 2. Escopo

Dentro:

- Tabela KV `app_setting` + hash bcrypt da master sob a chave
  `master_password_hash`.
- Pacote `internal/settings` (repository + handler) montado em `/api/settings`.
- Endpoint `POST /api/folders/{id}/reset-password` (recuperação).
- Coluna `folder.password_hint` + validação "dica ≠ senha".
- Área de Configurações no frontend (4º view, lazy) com seção de master password
  e lista de pastas bloqueadas com reset por-pasta.
- Round-trip de `app_setting` e `password_hint` no backup (3 modos).

Fora (documentado, não esquecido):

- "Limpar todas as pastas de uma vez" — reset é por-pasta, cirúrgico.
- Recuperar a própria master esquecida — volta a ser edição direta no banco (é o
  segredo raiz).
- Dica para a master (só para pastas).
- Cascata de proteção para subpastas (herdado do ADR-28).

## 3. Modelo de dados (migration 000016)

```sql
CREATE TABLE app_setting (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE folder ADD COLUMN password_hint TEXT;
```

- `app_setting` é uma tabela KV **genérica** — future-proof para outras settings
  singleton sem nova migração. Única chave hoje: `master_password_hash` (bcrypt).
- `folder.password_hint` é **não-secreta**: retornada verbatim em toda resposta
  de folder. `NULL` = sem dica.

## 4. Backend

### 4.1 `internal/pkg/pwhash` (novo leaf)
`Hash(plain) (string, error)` / `Verify(hash, plain) bool` — bcrypt em
`DefaultCost`. Compartilhado por `folders` (que agora delega
`HashPassword`/`VerifyPassword` a ele) e `settings`. Um único ponto de hash para
toda senha do app; evita `settings` importar `folders` só para hashear.

### 4.2 `internal/settings`
- `Repository`: `MasterPasswordConfigured`, `MasterPasswordHint`,
  `SetMasterPassword(ctx, plain, hint *string)` (upsert do hash **e** do hint na
  MESMA tx; hint nil = apaga), `ClearMasterPassword` (apaga hash **e** hint),
  `VerifyMaster(ctx, plain) → (ok, configured, err)`. O plaintext é hasheado e
  descartado; o hash nunca sai do backend. Chaves: `master_password_hash` +
  `master_password_hint`.
- `Handler` em `/api/settings`:
  - `GET /master-password` → `{configured, hint}` (a dica não-secreta; nunca o hash).
  - `PUT /master-password` → body `{password, current_password?, hint?}`.
    Primeiro-set não exige atual; trocar exige `current_password`
    (`401 wrong_password`); `minMasterPasswordLen = 8`; hint ≠ senha (`400`),
    hint ≤ 200, blank → nil.
  - `DELETE /master-password` → exige `current_password`; idempotente se não
    configurada.
- **UI-only (não no backend):** medidor de complexidade (`web/src/lib/
  passwordStrength.ts` + `PasswordStrength.tsx`) e campo "confirmar senha" — o
  backend só força o comprimento mínimo; o medidor é orientação.

### 4.3 `internal/folders` (alterações)
- `Folder.PasswordHint *string` (`json:"password_hint,omitempty"`).
- DTOs: `CreateInput.PasswordHint`; `UpdateInput.PasswordHint`/`PasswordHintSet`
  (tri-state). Validação de comprimento (≤200) e "dica ≠ senha".
- Repository:
  - `Create`/`Update`/`List`/`Get` selecionam e devolvem `password_hint`. **Não
    redigida** (ao contrário de `password_hash`).
  - `checkHintNotPassword(tx, id, passwordSet, newHash, hint)` roda dentro da tx
    SERIALIZABLE: computa o `effectiveHash` (novo se `PasswordSet`, senão o atual)
    e rejeita se `VerifyPassword(effectiveHash, hint)` — pega igualdade mesmo sem
    o plaintext. Dica em pasta sem senha efetiva → `400 invalid_input`.
  - `ResetPasswordByMaster(id)` → `UPDATE folder SET password_hash=NULL,
    password_hint=NULL`. Como o HMAC do token inclui o hash, zerá-lo invalida
    todo token vivo (propriedade herdada do ADR-28).
- Handler: `MasterPasswordVerifier` (interface estreita, consumer-side — evita
  import cycle `folders`↔`settings`), injetada em `NewHandler`. Nova rota
  `POST /{id}/reset-password`: verifica master → `400 master_not_configured` /
  `401 wrong_master_password` → `ResetPasswordByMaster`.

### 4.4 Wiring
`server.New` cria `settings.NewRepository(pool)`, monta `/api/settings`, e injeta
o repo como `MasterPasswordVerifier` em `folders.NewHandler`. Sem env nova (a
master é DB). `main.go` inalterado.

### 4.5 Backup
`backup.FolderRow.PasswordHint` + `backup.AppSettingRow` (`app_settings` no
`Snapshot`). Round-trip **verbatim** nos 3 modos: wipe TRUNCATEa `app_setting` e
restaura o do zip (inclusive "sem master" para backup antigo); skip/duplicate
usam `ON CONFLICT (key) DO NOTHING` (setting singleton não se duplica).
`DatabaseSnapshotVersion` 4→5; `CurrentSchemaVersion` 10→11.

## 5. Frontend

- `web/src/api/settings.ts`: `useMasterPasswordStatus`, `useSetMasterPassword`,
  `useRemoveMasterPassword`. `useResetFolderPassword` vive em `api/folders.ts`
  (invalida `['folders']`/`['entries']`).
- `web/src/pages/SettingsPage.tsx` (4º view lazy, padrão `ImportPage`):
  - **Master password**: status configurada/não; set/trocar/remover.
  - **Pastas bloqueadas**: lista `has_password`; reset por-pasta pedindo a
    master. Após o reset, a pasta sai da lista `locked` no refetch — a seção
    mantém uma linha "done" persistente (com "definir nova senha") em estado
    separado para não sumir.
- `FolderDialog.tsx`: campo de dica (create/first-set e edição standalone da
  dica em pasta protegida); validação client-side "dica ≠ senha" (o backend é a
  autoridade).
- `PasswordPromptDialog.tsx`: exibe `folder.password_hint` quando presente.
- Tipos em `api/types.ts`; i18n `settings.*` + extensões de `folder_dialog.*` /
  `folder_lock.*` / `topbar.settings`, paridade en/pt/es.

## 6. Modelo de ameaça

Igual ao ADR-28 (single-user, rede local, sem exposição pública). A master é a
**chave de recuperação raiz**: comprimento ≥8, nunca logada, nunca retornada, cap
de body 64 KiB nos handlers. Expor a dica reduz um pouco o sigilo, mas é o
propósito da dica e é aceitável no modelo. O zip de backup passa a carregar o
hash da master (como já carregava os de pasta) — mesma postura.

## 7. Testes

- Backend (integration): `settings` repo (lifecycle) + handler (set/troca/remove,
  min-length, 401s); folders reset (sucesso, `master_not_configured`,
  `wrong_master_password`), hint (≠ senha no create/update, remoção limpa dica,
  dica em pasta sem senha rejeitada); backup round-trip (hint + master) no wipe.
- Frontend (Vitest): hooks de settings + reset; `SettingsPage` (estados da
  master, lista+reset, erros); `FolderDialog` (campo dica, ≠ senha, payloads);
  `PasswordPromptDialog` (exibe/oculta dica). Mock em `src/test/server.ts`.
