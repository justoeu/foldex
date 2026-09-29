#!/usr/bin/env bash
# Shared by load/k6/run.sh. A terminal run may provision one local load account.
# The password stays in load/k6/.test-user (mode 600, gitignored).
if [[ -z "${ROOT:-}" ]]; then
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fi

k6_test_user_email() {
  printf '%s' "${K6_TEST_USER_EMAIL:-k6-load@foldex.local}"
}

k6_test_user_file() {
  printf '%s' "${K6_TEST_USER_FILE:-$ROOT/load/k6/.test-user}"
}

k6_base_url() {
  printf '%s' "${K6_BASE_URL:-http://127.0.0.1:9089}"
}

# Loopback is the only target this file will provision. A remote base URL
# would otherwise get a local database row that the load never logs into.
k6_target_is_loopback() {
  local host
  host="$(K6_BASE_URL="$(k6_base_url)" python3 -c 'import os, urllib.parse; print(urllib.parse.urlparse(os.environ["K6_BASE_URL"]).hostname or "")')"
  case "$host" in
    localhost|127.0.0.1|::1) return 0 ;;
    *) return 1 ;;
  esac
}

k6_is_yes() {
  local answer
  answer="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "$answer" in
    s|sim|y|yes) return 0 ;;
    *) return 1 ;;
  esac
}

# $1 prompt, $2 name of the override variable (1/yes uses, 0/no skips, empty asks).
k6_prompt_yes() {
  local prompt="$1" var_name="$2" value answer
  case "$var_name" in
    K6_TEST_USER) value="${K6_TEST_USER-}" ;;
    K6_DELETE_TEST_USER) value="${K6_DELETE_TEST_USER-}" ;;
    *)
      echo "pergunta desconhecida." >&2
      return 1
      ;;
  esac
  # 1/0 are the non-interactive switches. The words match k6_is_yes, in any case.
  # Anything else, including empty, only asks when a terminal is attached.
  local norm
  norm="$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]')"
  case "$norm" in
    1|s|sim|y|yes) return 0 ;;
    0|n|no|nao|não) return 1 ;;
    "") ;;
    *) return 1 ;;
  esac
  if [[ -t 0 ]]; then
    read -r -p "$prompt" answer || return 1
  elif [[ -t 1 && -r /dev/tty ]]; then
    read -r -p "$prompt" answer < /dev/tty || return 1
  else
    return 1
  fi
  k6_is_yes "$answer"
}

k6_read_saved_password() {
  local file line
  file="$(k6_test_user_file)"
  [[ -f "$file" ]] || return 1
  line="$(sed -n 's/^password=//p' "$file" | head -n 1)"
  [[ -n "$line" ]] || return 1
  printf '%s' "$line"
}

k6_export_saved_credentials() {
  local password
  K6_EMAIL="$(k6_test_user_email)"
  password="$(k6_read_saved_password)" || return 1
  K6_PASSWORD="$password"
  export K6_EMAIL K6_PASSWORD
}

# Prints one token: ok, missing, unauthorized, two_factor, rate_limited,
# unreachable, other. Never prints the body.
k6_classify_login() {
  local file email password payload body code status
  file="$(k6_test_user_file)"
  if [[ ! -f "$file" ]]; then
    printf '%s' missing
    return 0
  fi
  email="$(sed -n 's/^email=//p' "$file" | head -n 1)"
  password="$(k6_read_saved_password)" || {
    printf '%s' missing
    return 0
  }
  if [[ "$email" != "$(k6_test_user_email)" ]]; then
    printf '%s' other
    return 0
  fi
  payload="$(mktemp)"
  body="$(mktemp)"
  chmod 600 "$payload" "$body"
  # Password stays in the environment and the temp file, not on the argv of curl.
  K6_LOGIN_PAYLOAD="$payload" K6_EMAIL="$email" K6_PASSWORD="$password" python3 -c 'import json, os
open(os.environ["K6_LOGIN_PAYLOAD"], "w").write(json.dumps({"email": os.environ["K6_EMAIL"], "password": os.environ["K6_PASSWORD"]}))'
  # --noproxy keeps the password off http_proxy even when the base URL is loopback.
  local curl_args=(-sS --noproxy '*' --max-time 20 -o "$body" -w '%{http_code}' -X POST "$(k6_base_url)/api/auth/login" -H 'Content-Type: application/json' -H 'Accept: application/json' --data-binary "@$payload")
  if k6_target_is_loopback; then
    curl_args+=(--insecure)
  fi
  if ! code="$(curl "${curl_args[@]}")"; then
    rm -f "$payload" "$body"
    printf '%s' unreachable
    return 0
  fi
  rm -f "$payload"
  status="$(K6_LOGIN_BODY="$body" K6_LOGIN_CODE="$code" python3 -c 'import json, os
code = os.environ["K6_LOGIN_CODE"]
try:
    body = json.load(open(os.environ["K6_LOGIN_BODY"]))
except Exception:
    body = {}
status = str(body.get("status") or "")
if code == "200" and status == "authenticated":
    print("ok")
elif code == "200" and "two_factor" in status:
    print("two_factor")
elif code == "401":
    print("unauthorized")
elif code == "429":
    print("rate_limited")
else:
    print("other")
')"
  rm -f "$body"
  printf '%s' "$status"
}

k6_hash_password() {
  local passfile="$1" hash
  if ! hash="$(cd "$ROOT/backend" && go run "$ROOT/load/k6/hashpw.go" "$passfile")"; then
    echo "não consegui gerar o hash da senha." >&2
    return 1
  fi
  printf '%s' "$hash"
}

k6_backend_container() {
  local names
  if [[ -n "${K6_DB_CONTAINER:-}" ]]; then
    case "$K6_DB_CONTAINER" in
      foldex-backend|foldex-backend-*) printf '%s' "$K6_DB_CONTAINER"; return 0 ;;
      *)
        echo "container recusado: só foldex-backend." >&2
        return 1
        ;;
    esac
  fi
  names="$(docker ps --filter label=com.docker.compose.project=foldex --filter label=com.docker.compose.service=backend --format '{{.Names}}')" || return 1
  printf '%s\n' "$names" | grep -m 1 . || true
}

k6_run_db() {
  local action="$1" container url
  container="$(k6_backend_container)" || true
  if [[ -z "$container" ]]; then
    echo "não achei o container do backend local (foldex-backend)." >&2
    return 1
  fi
  if ! url="$(docker exec "$container" sh -c 'printf %s "$DB_URL"')"; then
    echo "não li o banco a partir do container ${container}." >&2
    return 1
  fi
  if [[ -z "$url" ]]; then
    echo "o container ${container} não tem DB_URL." >&2
    return 1
  fi
  local network
  network="$(docker inspect "$container" --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}' | grep -m 1 . || true)"
  FOLDEX_K6_NETWORK="$network" FOLDEX_K6_DB_URL="$url" python3 "$ROOT/load/k6/test_user_db.py" "$action"
  local code=$?
  unset FOLDEX_K6_DB_URL
  return "$code"
}

k6_db_upsert() {
  local email="$1" hash="$2"
  FOLDEX_K6_EMAIL="$email" FOLDEX_K6_PWHASH="$hash" k6_run_db upsert
}

k6_db_delete() {
  local email="$1"
  FOLDEX_K6_EMAIL="$email" k6_run_db delete
}

k6_write_credentials() {
  local dest="$1" email="$2" password="$3"
  umask 077
  printf 'email=%s\npassword=%s\n' "$email" "$password" > "$dest"
  chmod 600 "$dest"
}

k6_create_test_user() {
  local email password tmp hash passonly
  email="$(k6_test_user_email)"
  if [[ "$email" != "k6-load@foldex.local" ]]; then
    echo "recuso criar outro endereço que não k6-load@foldex.local." >&2
    return 1
  fi
  if ! k6_target_is_loopback; then
    echo "só crio o usuário de teste quando K6_BASE_URL é a API local (127.0.0.1). Para outro alvo, exporte K6_EMAIL e K6_PASSWORD." >&2
    return 1
  fi
  password="$(python3 -c 'import secrets; print(secrets.token_urlsafe(24), end="")')"
  tmp="$(mktemp)"
  passonly="$(mktemp)"
  chmod 600 "$passonly"
  printf '%s' "$password" > "$passonly"
  k6_write_credentials "$tmp" "$email" "$password"
  # Hash only the password. The credential file also carries the e-mail line.
  hash="$(k6_hash_password "$passonly")" || {
    rm -f "$tmp" "$passonly"
    return 1
  }
  rm -f "$passonly"
  if ! k6_db_upsert "$email" "$hash"; then
    rm -f "$tmp"
    return 1
  fi
  mv "$tmp" "$(k6_test_user_file)"
  chmod 600 "$(k6_test_user_file)"
  echo "usuário de teste criado: ${email}"
}

# Asks, reuses a working login, and creates the account only when that login fails.
k6_prepare_test_user() {
  K6_TEST_USER_MANAGED=0
  if ! k6_prompt_yes "Criar ou reutilizar o usuário de teste? [s/N] " K6_TEST_USER; then
    return 0
  fi
  # Before any login. A saved password must not be posted at a non-local API,
  # and a later delete must not remove the local row because a remote login worked.
  if ! k6_target_is_loopback; then
    echo "só uso o usuário de teste quando K6_BASE_URL é a API local (127.0.0.1). Para outro alvo, exporte K6_EMAIL e K6_PASSWORD." >&2
    return 1
  fi
  local result email
  email="$(k6_test_user_email)"
  result="$(k6_classify_login)"
  case "$result" in
    ok)
      k6_export_saved_credentials
      K6_TEST_USER_MANAGED=1
      echo "usuário de teste em uso: ${email}"
      return 0
      ;;
    unreachable)
      echo "não alcancei $(k6_base_url); não vou criar o usuário de teste." >&2
      return 1
      ;;
    rate_limited)
      echo "o login está limitado (429). Espere o Retry-After antes de criar de novo." >&2
      return 1
      ;;
    missing)
      echo "usuário de teste ainda não existe; criando a conta."
      ;;
    unauthorized|two_factor)
      echo "login do usuário de teste falhou (${result}); criando a conta."
      ;;
    *)
      echo "login do usuário de teste respondeu ${result}; não vou criar a conta." >&2
      return 1
      ;;
  esac
  k6_create_test_user || return 1
  result="$(k6_classify_login)"
  if [[ "$result" != "ok" ]]; then
    K6_TEST_USER_MANAGED=1
    echo "a conta foi gravada, mas o login ainda responde ${result}." >&2
    return 1
  fi
  k6_export_saved_credentials
  K6_TEST_USER_MANAGED=1
}

k6_finish_test_user() {
  [[ "${K6_TEST_USER_MANAGED:-0}" == "1" ]] || return 0
  local email
  email="$(k6_test_user_email)"
  if ! k6_prompt_yes "Apagar o usuário de teste ${email}? [s/N] " K6_DELETE_TEST_USER; then
    echo "usuário de teste mantido: ${email}"
    return 0
  fi
  if ! k6_db_delete "$email"; then
    echo "não apaguei ${email}. A senha continua em $(k6_test_user_file)." >&2
    return 1
  fi
  rm -f "$(k6_test_user_file)"
  echo "usuário de teste apagado: ${email}"
}
