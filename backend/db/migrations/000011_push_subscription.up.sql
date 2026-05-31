-- 000011_push_subscription.up.sql
--
-- Web Push subscriptions (RFC 8030 + VAPID). Single-user pelo modelo da §0 do
-- CLAUDE.md, então não há user_id ainda — adicionar quando o threat model
-- evoluir. `endpoint` é UNIQUE para suportar upsert: quando o navegador renova
-- a subscription, mantemos o mesmo row e só atualizamos p256dh/auth.
--
-- O sender remove o row em 404/410 (gone) — convenção do Web Push para "este
-- endpoint não existe mais, não me mande mais nada".

CREATE TABLE push_subscription (
    id           BIGSERIAL PRIMARY KEY,
    endpoint     TEXT NOT NULL,
    p256dh       TEXT NOT NULL,
    auth         TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    CONSTRAINT push_subscription_endpoint_unique UNIQUE (endpoint)
);
