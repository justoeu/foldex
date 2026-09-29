#!/usr/bin/env python3
"""Create or delete the k6 load account in the local Foldex database.

The URL arrives in FOLDEX_K6_DB_URL. This process never prints it, the
password, or the hash. stdout is `user_id=<n>` on upsert, empty on delete.
"""
import os
import subprocess
import sys
import urllib.parse

ALLOWED_EMAIL = "k6-load@foldex.local"


def quote(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def fail(message: str) -> None:
    sys.stderr.write(message.rstrip() + "\n")
    raise SystemExit(1)


def dial_plan(hostname: str) -> tuple[str, str]:
    # Only the database that belongs to this machine. `db` / `foldex-db` are
    # the compose service, reached on the backend's docker network. Loopback
    # and host.docker.internal are a published port on the host. Anything else
    # is some other server, and this script will not write there.
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
    # The URL and the password must not survive in the child environment.
    env.pop("FOLDEX_K6_DB_URL", None)
    env.pop("FOLDEX_K6_PWHASH", None)
    return host, port, user, dbname, env, mode


def psql_args(host, port, user, dbname) -> list[str]:
    return [
        "-h", host, "-p", port, "-U", user, "-d", dbname,
        "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-1",
    ]


def psql(env, host, port, user, dbname, mode, sql: str) -> str:
    if mode == "docker":
        network = os.environ.get("FOLDEX_K6_NETWORK", "")
        if not network:
            fail("não achei a rede do backend para alcançar o Postgres")
        image = os.environ.get("FOLDEX_K6_PSQL_IMAGE", "postgres:18.6-alpine")
        # -e NAME (no value) copies PGPASSWORD from this process. It is not an argv secret.
        cmd = [
            "docker", "run", "--rm", "--network", network,
            "--add-host", "host.docker.internal:host-gateway",
            "-e", "PGPASSWORD", "-e", "PGSSLMODE", "-i",
            "--entrypoint", "psql", image,
            *psql_args(host, port, user, dbname),
        ]
    else:
        cmd = ["psql", *psql_args(host, port, user, dbname)]
    proc = subprocess.run(cmd, input=sql, text=True, capture_output=True, env=env)
    if proc.returncode != 0:
        # Server stderr can include the failing row, hash included. Do not print it.
        fail("psql falhou")
    return proc.stdout.strip()


def email_of() -> str:
    email = os.environ.get("FOLDEX_K6_EMAIL", ALLOWED_EMAIL).strip().lower()
    if email != ALLOWED_EMAIL:
        fail("recuso mexer em outro endereço que não o usuário de teste")
    return email


def upsert(env, host, port, user, dbname, mode) -> None:
    email = email_of()
    pw_hash = os.environ.get("FOLDEX_K6_PWHASH", "").strip()
    if not pw_hash.startswith("$2"):
        fail("hash de senha inválido")
    role = psql(
        env, host, port, user, dbname, mode,
        f"SELECT COALESCE((SELECT role FROM app_user WHERE email_normalized = {quote(email)}), '')",
    )
    if role == "owner":
        fail("recuso alterar a conta owner")
    # Reset factors on this address only. A load run cannot pass a second factor,
    # and a previous experiment may have enrolled one on the same mailbox.
    # psql -1 runs this as one transaction. Factors go before sessions: the
    # e-mail factor row points at a session, and deleting both in one CTE can
    # touch the same row twice.
    sql = f"""
INSERT INTO app_user (
    email, email_normalized, name, role, status, password_hash, email_verified_at
) VALUES (
    {quote(email)}, {quote(email)}, 'k6 load', 'editor', 'active', {quote(pw_hash)}, now()
)
ON CONFLICT (email_normalized) DO UPDATE
   SET password_hash = EXCLUDED.password_hash,
       status = 'active',
       role = 'editor',
       name = EXCLUDED.name,
       email_verified_at = COALESCE(app_user.email_verified_at, now()),
       token_version = app_user.token_version + 1,
       updated_at = now()
   WHERE app_user.role <> 'owner'
RETURNING id;
DELETE FROM email_factor WHERE user_id = (SELECT id FROM app_user WHERE email_normalized = {quote(email)});
DELETE FROM totp_secret WHERE user_id = (SELECT id FROM app_user WHERE email_normalized = {quote(email)});
DELETE FROM recovery_code WHERE user_id = (SELECT id FROM app_user WHERE email_normalized = {quote(email)});
DELETE FROM session WHERE user_id = (SELECT id FROM app_user WHERE email_normalized = {quote(email)});
"""
    user_id = psql(env, host, port, user, dbname, mode, sql).splitlines()[0].strip()
    if not user_id.isdigit():
        fail("a conta não devolveu id")
    sys.stdout.write(f"user_id={user_id}\n")


def delete(env, host, port, user, dbname, mode) -> None:
    email = email_of()
    role = psql(
        env, host, port, user, dbname, mode,
        f"SELECT COALESCE((SELECT role FROM app_user WHERE email_normalized = {quote(email)}), '')",
    )
    if role == "owner":
        fail("recuso apagar a conta owner")
    psql(
        env, host, port, user, dbname,
        mode,
        f"DELETE FROM app_user WHERE email_normalized = {quote(email)} AND role <> 'owner' RETURNING id",
    )


def main() -> None:
    if len(sys.argv) == 3 and sys.argv[1] == "classify-host":
        host, mode = dial_plan(sys.argv[2])
        sys.stdout.write(f"{mode} {host}\n")
        return
    if len(sys.argv) != 2 or sys.argv[1] not in ("upsert", "delete"):
        fail("uso: test_user_db.py upsert|delete")
    host, port, user, dbname, env, mode = connect()
    if sys.argv[1] == "upsert":
        upsert(env, host, port, user, dbname, mode)
    else:
        delete(env, host, port, user, dbname, mode)


if __name__ == "__main__":
    main()
