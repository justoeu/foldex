#!/usr/bin/env bash
# Decision table for the k6 test-user prompt. No database and no k6.
# Run with: bash scripts/test-k6-test-user.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=../load/k6/test-user.sh
source "$ROOT/load/k6/test-user.sh"
eval "$(declare -f k6_classify_login | sed '1s/^k6_classify_login /k6_classify_login_real /')"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# Command substitution would hide exports: prepare must run in this shell.
run_prep() {
  k6_prepare_test_user >"$tmpdir/out" 2>&1
}
run_finish() {
  k6_finish_test_user >"$tmpdir/out" 2>&1
}
show() {
  cat "$tmpdir/out"
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

reset_stubs() {
  UPSERTS=0
  DELETES=0
  LOGIN_MODE="${1:-ok}"
  printf '0\n' > "$tmpdir/login-calls"
  LOGIN_CALLS=0
  K6_TEST_USER_MANAGED=0
  K6_TEST_USER_FILE="$tmpdir/creds"
  K6_BASE_URL="http://127.0.0.1:9089"
  unset K6_EMAIL K6_PASSWORD K6_TEST_USER K6_DELETE_TEST_USER
  unset K6_TEST_USER_EMAIL K6_TEST_USER_I_MEAN_THIS_EMAIL
  rm -f "$K6_TEST_USER_FILE"
}

k6_classify_login() {
  # prepare captures this function in a subshell, so the counter lives in a file.
  LOGIN_CALLS="$(cat "$tmpdir/login-calls")"
  LOGIN_CALLS=$((LOGIN_CALLS + 1))
  printf '%s\n' "$LOGIN_CALLS" > "$tmpdir/login-calls"
  case "$LOGIN_MODE" in
    then-ok)
      if [[ "$LOGIN_CALLS" == 1 ]]; then
        printf '%s' unauthorized
      else
        printf '%s' ok
      fi
      ;;
    missing-then-ok)
      if [[ "$LOGIN_CALLS" == 1 ]]; then
        printf '%s' missing
      else
        printf '%s' ok
      fi
      ;;
    two-then-ok)
      if [[ "$LOGIN_CALLS" == 1 ]]; then
        printf '%s' two_factor
      else
        printf '%s' ok
      fi
      ;;
    *) printf '%s' "$LOGIN_MODE" ;;
  esac
}

# shellcheck disable=SC2016 # bcrypt marker, not a shell expansion
stub_hash='$2a$10$stubhashstubhashstubhashstubh'
k6_hash_password() {
  cat "$1" > "$tmpdir/hashed"
  printf '%s' "$stub_hash"
}

k6_db_upsert() {
  UPSERTS=$((UPSERTS + 1))
  [[ "$1" == "k6-load@foldex.local" ]] || return 1
  [[ "$2" == "$stub_hash" ]] || return 1
  return 0
}

k6_db_delete() {
  DELETES=$((DELETES + 1))
  [[ "$1" == "k6-load@foldex.local" ]] || return 1
  return 0
}

password_of() {
  [[ -f "$K6_TEST_USER_FILE" ]] || return 0
  sed -n 's/^password=//p' "$K6_TEST_USER_FILE" | head -n 1
}

assert_secret_hidden() {
  local out="$1" secret
  secret="$(password_of || true)"
  if [[ -n "$secret" && "$out" == *"$secret"* ]]; then
    fail "a senha apareceu na saída"
  fi
}

mode_of() {
  stat -f '%OLp' "$1" 2>/dev/null || stat -c '%a' "$1"
}

# Opting out never logs in and never writes a password.
reset_stubs ok
K6_TEST_USER=0
run_prep || fail "opt-out falhou: $(show)"
out="$(show)"
[[ "$(cat "$tmpdir/login-calls")" == 0 ]] || fail "opt-out chamou o login"
[[ "$UPSERTS" == 0 ]] || fail "opt-out criou conta"
[[ "$K6_TEST_USER_MANAGED" == 0 ]] || fail "opt-out marcou a conta como gerida"
[[ -z "$out" ]] || fail "opt-out escreveu na saída: $out"

# A working login is reused. The database is not touched.
reset_stubs ok
printf 'email=k6-load@foldex.local\npassword=already-known-secret\n' > "$K6_TEST_USER_FILE"
chmod 600 "$K6_TEST_USER_FILE"
K6_TEST_USER=1
K6_DELETE_TEST_USER=0
run_prep || fail "reuso falhou: $(show)"
out="$(show)"
[[ "$out" == *"em uso: k6-load@foldex.local"* ]] || fail "não reutilizou: $out"
[[ "$UPSERTS" == 0 ]] || fail "reuso gravou no banco"
[[ "$K6_EMAIL" == "k6-load@foldex.local" ]] || fail "não exportou o e-mail"
[[ "$K6_PASSWORD" == "already-known-secret" ]] || fail "não exportou a senha salva"
[[ "$K6_TEST_USER_MANAGED" == 1 ]] || fail "reuso não ficou gerido"
assert_secret_hidden "$out"
run_finish || fail "finish falhou: $(show)"
out="$(show)"
[[ "$DELETES" == 0 ]] || fail "manter a conta apagou o banco"
[[ -f "$K6_TEST_USER_FILE" ]] || fail "manter a conta apagou o arquivo"
[[ "$out" == *"mantido"* ]] || fail "não avisou que manteve: $out"

# A failed login creates the account, then a second login has to succeed.
reset_stubs then-ok
K6_TEST_USER=1
run_prep || fail "criação falhou: $(show)"
out="$(show)"
[[ "$UPSERTS" == 1 ]] || fail "falha de login não criou (upserts=$UPSERTS)"
LOGIN_CALLS="$(cat "$tmpdir/login-calls")"
[[ "$LOGIN_CALLS" == 2 ]] || fail "não tentou o login de novo (calls=$LOGIN_CALLS)"
[[ "$out" == *"criado: k6-load@foldex.local"* ]] || fail "não avisou a criação: $out"
[[ -f "$K6_TEST_USER_FILE" ]] || fail "não gravou a credencial"
[[ "$(mode_of "$K6_TEST_USER_FILE")" == "600" ]] || fail "credencial não ficou 600"
assert_secret_hidden "$out"
[[ -n "$(password_of)" ]] || fail "arquivo sem senha"
[[ "$(cat "$tmpdir/hashed")" == "$(password_of)" ]] || fail "o hash não foi calculado só com a senha"
[[ "$K6_EMAIL" == "k6-load@foldex.local" ]] || fail "criação não exportou o e-mail"
[[ "$K6_PASSWORD" == "$(password_of)" ]] || fail "criação não exportou a senha"
[[ "$K6_TEST_USER_MANAGED" == 1 ]] || fail "criação não ficou gerida"

# Missing file is the same path as a failed login.
reset_stubs missing-then-ok
K6_TEST_USER=1
run_prep || fail "ausente falhou: $(show)"
[[ "$UPSERTS" == 1 ]] || fail "arquivo ausente não criou"

# Second factor on this mailbox is reset by creating again.
reset_stubs two-then-ok
K6_TEST_USER=1
run_prep || fail "2FA falhou: $(show)"
[[ "$UPSERTS" == 1 ]] || fail "2FA não recriou a conta"

# The API being down, or a 429, must not invent a password.
for mode in unreachable rate_limited other; do
  reset_stubs "$mode"
  K6_TEST_USER=1
  if run_prep; then
    fail "$mode deveria abortar"
  fi
  [[ "$UPSERTS" == 0 ]] || fail "$mode criou a conta"
  [[ ! -f "$K6_TEST_USER_FILE" ]] || fail "$mode gravou senha"
done

# A remote target is not provisioned in the local database.
reset_stubs unauthorized
K6_BASE_URL="https://foldex.example"
K6_TEST_USER=1
if run_prep; then
  fail "alvo remoto deveria abortar"
fi
[[ "$UPSERTS" == 0 ]] || fail "alvo remoto gravou no banco local"
[[ "$(cat "$tmpdir/login-calls")" == 0 ]] || fail "alvo remoto tentou login"

# Deleting removes the row and the local secret.
reset_stubs ok
printf 'email=k6-load@foldex.local\npassword=already-known-secret\n' > "$K6_TEST_USER_FILE"
chmod 600 "$K6_TEST_USER_FILE"
K6_TEST_USER=1
K6_DELETE_TEST_USER=1
run_prep || fail "prepare antes de apagar falhou: $(show)"
run_finish || fail "apagar falhou: $(show)"
out="$(show)"
[[ "$DELETES" == 1 ]] || fail "não apagou"
[[ ! -f "$K6_TEST_USER_FILE" ]] || fail "arquivo sobreviveu ao apagar"
[[ "$out" == *"apagado: k6-load@foldex.local"* ]] || fail "não avisou o apagar: $out"
assert_secret_hidden "$out"

# Another mailbox is refused. There is no override.
reset_stubs missing-then-ok
K6_TEST_USER=1
K6_TEST_USER_EMAIL="owner@example.com"
if k6_create_test_user >/dev/null 2>&1; then
  fail "aceitou outro e-mail"
fi
[[ "$UPSERTS" == 0 ]] || fail "outro e-mail chegou no banco"

# Prompt words. Empty and no stay negative.
k6_is_yes s || fail "s"
k6_is_yes SIM || fail "SIM"
k6_is_yes yes || fail "yes"
if k6_is_yes ""; then fail "vazio deveria ser não"; fi
if k6_is_yes nao; then fail "nao deveria ser não"; fi
k6_target_is_loopback || fail "127.0.0.1 deveria ser local"
K6_BASE_URL="http://localhost:9089"
k6_target_is_loopback || fail "localhost deveria ser local"
K6_BASE_URL="http://203.0.113.10:9089"
if k6_target_is_loopback; then fail "host remoto não é loopback"; fi

K6_TEST_USER=YES
k6_prompt_yes "x" K6_TEST_USER || fail "YES deveria aceitar"
K6_TEST_USER=Não
if k6_prompt_yes "x" K6_TEST_USER; then fail "Não deveria recusar"; fi

# A login that stays unauthorized still leaves the new account managed, so the
# exit prompt can delete the password it just wrote.
reset_stubs unauthorized
K6_TEST_USER=1
if run_prep; then fail "segundo login ruim deveria abortar"; fi
[[ "$K6_TEST_USER_MANAGED" == 1 ]] || fail "conta gravada sem ficar gerida"
[[ -f "$K6_TEST_USER_FILE" ]] || fail "senha sumiu depois do login ruim"

# Deleting is a no-op when this run did not take the test account.
reset_stubs ok
K6_TEST_USER_MANAGED=0
K6_DELETE_TEST_USER=1
run_finish || fail "finish sem conta gerida falhou"
[[ "$DELETES" == 0 ]] || fail "finish sem conta gerida apagou"

# A failed delete keeps the local secret.
(
  k6_db_delete() { return 1; }
  reset_stubs ok
  printf 'email=k6-load@foldex.local\npassword=keep-me\n' > "$K6_TEST_USER_FILE"
  chmod 600 "$K6_TEST_USER_FILE"
  K6_TEST_USER_MANAGED=1
  K6_DELETE_TEST_USER=1
  if k6_finish_test_user >"$tmpdir/out" 2>&1; then
    echo "FAIL: delete que falha deveria abortar" >&2
    exit 1
  fi
  [[ -f "$K6_TEST_USER_FILE" ]] || {
    echo "FAIL: delete que falha apagou o arquivo" >&2
    exit 1
  }
)

# No terminal and no switch: do not ask, do not log in.
reset_stubs ok
k6_prepare_test_user </dev/null >"$tmpdir/out" 2>&1 || fail "sem tty deveria seguir"
[[ "$(cat "$tmpdir/login-calls")" == 0 ]] || fail "sem tty chamou o login"

# The real login classifier, with curl replaced. A 429 must not look like "no such user".
cat > "$tmpdir/curl" <<'EOF'
#!/bin/bash
out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [[ "${CURL_EXIT:-0}" != 0 ]]; then
  exit "$CURL_EXIT"
fi
printf '%s' "${CURL_BODY:-}" > "$out"
printf '%s' "${CURL_CODE:-200}"
EOF
chmod +x "$tmpdir/curl"
classify_with() {
  printf 'email=k6-load@foldex.local\npassword=secret\n' > "$K6_TEST_USER_FILE"
  PATH="$tmpdir:$PATH" CURL_CODE="$1" CURL_BODY="$2" CURL_EXIT="${3:-0}" k6_classify_login_real
}
reset_stubs ok
[[ "$(classify_with 200 '{"status":"authenticated"}')" == ok ]] || fail "200 authenticated"
[[ "$(classify_with 200 '{"status":"two_factor_required"}')" == two_factor ]] || fail "2FA"
[[ "$(classify_with 401 '{}')" == unauthorized ]] || fail "401"
[[ "$(classify_with 429 '{}')" == rate_limited ]] || fail "429"
[[ "$(classify_with 200 '{}')" == other ]] || fail "200 sem status"
[[ "$(classify_with 200 '{"status":"authenticated"}' 7)" == unreachable ]] || fail "curl caiu"
rm -f "$K6_TEST_USER_FILE"
[[ "$(PATH="$tmpdir:$PATH" k6_classify_login_real)" == missing ]] || fail "arquivo ausente"
printf 'email=other@example.com\npassword=x\n' > "$K6_TEST_USER_FILE"
[[ "$(PATH="$tmpdir:$PATH" k6_classify_login_real)" == other ]] || fail "e-mail trocado"

py="$ROOT/load/k6/test_user_db.py"
[[ "$(python3 "$py" classify-host db)" == "docker db" ]] || fail "db"
[[ "$(python3 "$py" classify-host foldex-db)" == "docker foldex-db" ]] || fail "foldex-db"
[[ "$(python3 "$py" classify-host host.docker.internal)" == "host 127.0.0.1" ]] || fail "docker host"
[[ "$(python3 "$py" classify-host ::1)" == "host ::1" ]] || fail "::1"
if python3 "$py" classify-host 8.8.8.8 >/dev/null 2>&1; then
  fail "host externo deveria ser recusado"
fi
grep -q 'token_version = app_user.token_version + 1' "$py" || fail "token_version não sobe"
grep -q -- "--noproxy" "$ROOT/load/k6/test-user.sh" || fail "login sem --noproxy"

echo "ok"
