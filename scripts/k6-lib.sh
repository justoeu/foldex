# shellcheck shell=bash
# Shared path resolution for the local k6 runners. Source this file.
# Run state (database URL, quota snapshot, logs) stays outside the repository.
# Machine addresses stay in load/k6/env.local, never in this file.

k6_repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

# Fill only names the caller has not already exported. A prefix on `source`
# does not survive on the bash this suite runs under, so callers export first
# and this file must not clobber that.
k6_load_operator_env() {
  local root="$1"
  local file="${K6_OPERATOR_ENV_FILE:-$root/load/k6/env.local}"
  [[ -f "$file" ]] || return 0
  local line key value
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%%$'\r'}"
    line="${line%%#*}"
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [[ -z "$line" ]] && continue
    if [[ "$line" == export\ * ]]; then
      line="${line#export }"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    [[ "$line" == *=* ]] || return 1
    key="${line%%=*}"
    key="${key%"${key##*[![:space:]]}"}"
    value="${line#*=}"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    if [[ "$value" == \"*\" ]]; then
      value="${value#\"}"
      value="${value%\"}"
    elif [[ "$value" == \'*\' ]]; then
      value="${value#\'}"
      value="${value%\'}"
    fi
    [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || return 1
    if [[ -z "${!key+x}" ]]; then
      export "$key=$value"
    fi
  done < "$file"
}

# Product default when the operator has not named an API. Trailing slash is
# stripped so callers can append /healthz and /metrics.
k6_origin() {
  local base="${K6_BASE_URL:-http://127.0.0.1:9089}"
  base="${base%/}"
  printf '%s\n' "$base"
}

k6_health_code() {
  local max="$1" code
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time "$max" "$(k6_origin)/healthz" 2>/dev/null)" || code="000"
  if [[ -z "$code" ]]; then
    code="000"
  fi
  printf '%s\n' "$code"
}

k6_require_health() {
  local max="${1:-5}"
  curl -fsS -o /dev/null --max-time "$max" "$(k6_origin)/healthz"
}

# Host publish spec for a throwaway backend. The process inside listens on
# 9089. These runners replace the local container, so a non-loopback origin
# is refused instead of publishing a socket the load will not call.
k6_publish_spec() {
  local origin hostport host port
  origin="$(k6_origin)"
  origin="${origin#http://}"
  origin="${origin#https://}"
  hostport="${origin%%/*}"
  if [[ "$hostport" == \[*\]* ]]; then
    host="${hostport%%]*}"
    host="${host#[}"
    port="${hostport##*:}"
  elif [[ "$hostport" == *:* ]]; then
    host="${hostport%%:*}"
    port="${hostport##*:}"
  else
    host="$hostport"
    port=80
  fi
  case "$host" in
    127.0.0.1|localhost) printf '%s:%s:9089\n' "$host" "$port" ;;
    ::1) printf '[::1]:%s:9089\n' "$port" ;;
    *)
      echo "K6_BASE_URL precisa ser loopback para substituir o backend local" >&2
      return 2
      ;;
  esac
}

# Postgres is omitted when FOLDEX_K6_POSTGRES is empty, so a machine that has
# not named that container still samples the backend.
k6_container_stats() {
  local box="$1"
  if [[ -n "${PGBOX:-}" ]]; then
    docker stats --no-stream --format '{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}' "$box" "$PGBOX"
  else
    docker stats --no-stream --format '{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}' "$box"
  fi
}

# Remote write stays off until a receiver URL exists. K6_GRAFANA=1 with an
# empty URL is an error: k6 would start and the dashboard would stay blank.
# Sets K6_OUT to the output flags (empty when remote write is off).
k6_prepare_remote_write() {
  local mode="${K6_GRAFANA-}"
  K6_OUT=()
  if [[ -z "$mode" ]]; then
    if [[ -n "${K6_PROMETHEUS_RW_SERVER_URL:-}" ]]; then
      mode=1
    else
      mode=0
    fi
  fi
  if [[ "$mode" == "0" ]]; then
    return 0
  fi
  if [[ -z "${K6_PROMETHEUS_RW_SERVER_URL:-}" ]]; then
    echo "K6_PROMETHEUS_RW_SERVER_URL ausente. Defina em load/k6/env.local ou exporte a variável. K6_GRAFANA=0 deixa só o terminal." >&2
    return 2
  fi
  export K6_PROMETHEUS_RW_SERVER_URL
  export K6_PROMETHEUS_RW_TREND_STATS="${K6_PROMETHEUS_RW_TREND_STATS:-p(95),p(99),max}"
  export K6_PROMETHEUS_RW_PUSH_INTERVAL="${K6_PROMETHEUS_RW_PUSH_INTERVAL:-5s}"
  K6_OUT=(-o experimental-prometheus-rw)
}

# The cert directory is whatever the stock container already mounts. A runner
# that starts a second container has to reuse that bind; it is not a path in
# this repository.
k6_certs_source() {
  local box="$1"
  docker inspect "$box" --format '{{range .Mounts}}{{if eq .Destination "/etc/foldex/certs"}}{{.Source}}{{end}}{{end}}'
}

k6_running_image() {
  local box="$1"
  docker inspect "$box" --format '{{.Config.Image}}'
}
