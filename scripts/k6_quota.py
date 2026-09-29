#!/usr/bin/env python3
"""Snapshot, raise, and restore the local Foldex write quota.

Stdout is a single line of integers. The database URL, the policy document,
and psql stderr are never printed. The host allow-list matches load/k6/test_user_db.py.
"""
import json
import os
import stat
import subprocess
import sys
import urllib.parse

KEY = "abuse_policy"
FIELD = "api_writes_per_minute"
ORIG = os.environ.get("FOLDEX_ORDERS_ORIG", "")
ABSENT = "ABSENT"


def target_writes() -> int:
    raw = os.environ.get("FOLDEX_ORDERS_TARGET", "6000")
    if not raw.isdigit():
        fail("alvo de cota inválido")
    value = int(raw)
    if value < 30 or value > 20000:
        fail("alvo de cota fora da janela")
    return value


def fail(message: str) -> None:
    sys.stderr.write(message.rstrip() + "\n")
    raise SystemExit(1)


def dial_plan(hostname: str) -> tuple[str, str]:
    if hostname in ("localhost", "host.docker.internal", "127.0.0.1"):
        return "127.0.0.1", "host"
    if hostname == "::1":
        return "::1", "host"
    if hostname in ("db", "foldex-db"):
        return hostname, "docker"
    fail("recuso gravar num Postgres fora da instância local")
    return "", ""


def connect():
    raw = os.environ.get("FOLDEX_K6_DB_URL", "")
    if not raw:
        fail("DB_URL ausente")
    parsed = urllib.parse.urlparse(raw)
    host, mode = dial_plan(parsed.hostname or "")
    query = urllib.parse.parse_qs(parsed.query)
    sslmode = (query.get("sslmode") or ["prefer"])[0]
    user = urllib.parse.unquote(parsed.username or "")
    password = urllib.parse.unquote(parsed.password or "")
    dbname = (parsed.path or "/foldex").lstrip("/") or "foldex"
    port = str(parsed.port or 5432)
    if not user:
        fail("DB_URL sem usuário")
    env = os.environ.copy()
    env["PGPASSWORD"] = password
    env["PGSSLMODE"] = sslmode
    env.pop("FOLDEX_K6_DB_URL", None)
    return host, port, user, dbname, env, mode


def psql(env, host, port, user, dbname, mode, sql: str) -> str:
    args = [
        "-h", host, "-p", port, "-U", user, "-d", dbname,
        "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-1",
    ]
    if mode == "docker":
        network = os.environ.get("FOLDEX_K6_NETWORK", "")
        if not network:
            fail("não achei a rede do backend para alcançar o Postgres")
        image = os.environ.get("FOLDEX_K6_PSQL_IMAGE", "postgres:18.6-alpine")
        cmd = [
            "docker", "run", "--rm", "--network", network,
            "--add-host", "host.docker.internal:host-gateway",
            "-e", "PGPASSWORD", "-e", "PGSSLMODE", "-i",
            "--entrypoint", "psql", image, *args,
        ]
    else:
        cmd = ["psql", *args]
    proc = subprocess.run(cmd, input=sql, text=True, capture_output=True, env=env)
    if proc.returncode != 0:
        fail("psql falhou")
    return proc.stdout.strip()


def quote_literal(value: str) -> str:
    if "$pol$" in value:
        fail("documento da política contém o delimitador")
    return "$pol$" + value + "$pol$"


def read_raw(env, host, port, user, dbname, mode) -> str:
    return psql(
        env, host, port, user, dbname, mode,
        f"SELECT COALESCE((SELECT value FROM app_setting WHERE key = '{KEY}'), '')",
    )


def read_writes(env, host, port, user, dbname, mode) -> str:
    got = psql(
        env, host, port, user, dbname, mode,
        "SELECT COALESCE((SELECT value::json->>'api_writes_per_minute' "
        f"FROM app_setting WHERE key = '{KEY}'), 'absent')",
    )
    if got != "absent" and not got.isdigit():
        fail("cota ilegível")
    return got


def write_doc(env, host, port, user, dbname, mode, document: str) -> None:
    sql = (
        "INSERT INTO app_setting (key, value) VALUES "
        f"('{KEY}', {quote_literal(document)}) "
        "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()"
    )
    psql(env, host, port, user, dbname, mode, sql)


def save_orig(raw: str) -> None:
    os.makedirs(os.path.dirname(ORIG), exist_ok=True)
    flags = os.O_WRONLY | os.O_CREAT | os.O_TRUNC
    fd = os.open(ORIG, flags, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write(ABSENT if raw == "" else raw)
    os.chmod(ORIG, stat.S_IRUSR | stat.S_IWUSR)


def show(env, host, port, user, dbname, mode) -> None:
    sys.stdout.write("writes=" + read_writes(env, host, port, user, dbname, mode) + "\n")


def raise_quota(env, host, port, user, dbname, mode) -> None:
    target = target_writes()
    before = read_writes(env, host, port, user, dbname, mode)
    raw = read_raw(env, host, port, user, dbname, mode)
    save_orig(raw)
    if raw == "":
        doc = {FIELD: target}
    else:
        try:
            parsed = json.loads(raw)
        except json.JSONDecodeError:
            fail("política armazenada não é JSON")
        if not isinstance(parsed, dict):
            fail("política armazenada não é um objeto")
        doc = parsed
        doc[FIELD] = target
    write_doc(env, host, port, user, dbname, mode, json.dumps(doc, separators=(",", ":")))
    now = read_writes(env, host, port, user, dbname, mode)
    if now != str(target):
        fail("a cota não ficou no alvo")
    sys.stdout.write(f"writes_before={before} writes_now={now}\n")


def restore(env, host, port, user, dbname, mode) -> None:
    if not os.path.isfile(ORIG):
        sys.stdout.write("writes_now=" + read_writes(env, host, port, user, dbname, mode) + "\n")
        return
    raw = open(ORIG).read()
    if raw == ABSENT:
        psql(
            env, host, port, user, dbname, mode,
            f"DELETE FROM app_setting WHERE key = '{KEY}'",
        )
    else:
        write_doc(env, host, port, user, dbname, mode, raw)
    sys.stdout.write("writes_now=" + read_writes(env, host, port, user, dbname, mode) + "\n")


def stats(env, host, port, user, dbname, mode) -> None:
    sql = """
SELECT
  (SELECT count(*) FROM link l
     JOIN app_user u ON u.id = l.user_id
    WHERE u.email_normalized = 'k6-load@foldex.local')::text,
  (SELECT count(*) FROM link l
     JOIN app_user u ON u.id = l.user_id
    WHERE u.email_normalized = 'k6-load@foldex.local' AND l.preview_status = 'pending')::text,
  (SELECT count(*) FROM link l
     JOIN app_user u ON u.id = l.user_id
    WHERE u.email_normalized = 'k6-load@foldex.local' AND l.preview_status = 'ok')::text,
  (SELECT count(*) FROM link l
     JOIN app_user u ON u.id = l.user_id
    WHERE u.email_normalized = 'k6-load@foldex.local' AND l.preview_status = 'failed')::text,
  (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'active')::text,
  (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'idle')::text,
  (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'idle in transaction')::text,
  (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database())::text,
  pg_database_size(current_database())::text,
  pg_total_relation_size('link')::text,
  (SELECT count(*) FROM audit_log WHERE actor_id = (
      SELECT id FROM app_user WHERE email_normalized = 'k6-load@foldex.local'))::text,
  (SELECT count(*) FROM audit_log)::text,
  (SELECT COALESCE((SELECT n_live_tup FROM pg_stat_user_tables
      WHERE schemaname = 'public' AND relname = 'link'), 0))::text,
  (SELECT COALESCE((SELECT n_dead_tup FROM pg_stat_user_tables
      WHERE schemaname = 'public' AND relname = 'link'), 0))::text,
  (SELECT blks_hit FROM pg_stat_database WHERE datname = current_database())::text,
  (SELECT blks_read FROM pg_stat_database WHERE datname = current_database())::text
"""
    parts = psql(env, host, port, user, dbname, mode, sql).split("|")
    if len(parts) != 16:
        fail("amostra incompleta")
    names = (
        "links", "preview_pending", "preview_ok", "preview_failed",
        "pg_active", "pg_idle", "pg_idle_xact", "pg_total",
        "db_bytes", "link_bytes", "user_audit", "audit_total",
        "link_live", "link_dead", "blks_hit", "blks_read",
    )
    sys.stdout.write(" ".join(f"{name}={value}" for name, value in zip(names, parts)) + "\n")


def dbinfo(env, host, port, user, dbname, mode) -> None:
    sql = "SELECT current_setting('max_connections'), current_setting('shared_buffers')"
    parts = psql(env, host, port, user, dbname, mode, sql).split("|")
    if len(parts) != 2:
        fail("dbinfo incompleto")
    sys.stdout.write(f"max_connections={parts[0]} shared_buffers={parts[1]}\n")


def main() -> None:
    if not ORIG:
        fail("FOLDEX_ORDERS_ORIG ausente")
    if len(sys.argv) != 2 or sys.argv[1] not in ("show", "raise", "restore", "stats", "dbinfo"):
        fail("uso: k6_quota.py show|raise|restore|stats|dbinfo")
    host, port, user, dbname, env, mode = connect()
    action = sys.argv[1]
    if action == "show":
        show(env, host, port, user, dbname, mode)
    elif action == "raise":
        raise_quota(env, host, port, user, dbname, mode)
    elif action == "restore":
        restore(env, host, port, user, dbname, mode)
    elif action == "stats":
        stats(env, host, port, user, dbname, mode)
    else:
        dbinfo(env, host, port, user, dbname, mode)


if __name__ == "__main__":
    main()
