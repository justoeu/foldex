package auth

import (
	"context"
	"fmt"

	"foldex/internal/auth/ipblock"
	"foldex/internal/pkg/authctx"
)

const maxIPBlockReason = 256

func (r *Repository) BlockIP(ctx context.Context, ip, reason string,
	by *authctx.UserID, byEmail string) (ipblock.IPBlock, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM ip_block`).Scan(&n); err != nil {
		return ipblock.IPBlock{}, fmt.Errorf("count ip blocks: %w", err)
	}
	if n >= ipblock.MaxIPBlocks {
		return ipblock.IPBlock{}, ipblock.ErrBlockFull
	}
	var actor *int64
	if by != nil {
		id := int64(*by)
		actor = &id
	}
	var out ipblock.IPBlock
	err := r.pool.QueryRow(ctx, `
		INSERT INTO ip_block (ip, reason, created_by, created_by_email)
		VALUES ($1::inet, NULLIF($2, ''), $3, NULLIF($4, ''))
		ON CONFLICT (ip) DO UPDATE SET ip = EXCLUDED.ip
		RETURNING id, host(ip), reason, created_by_email, created_at`,
		ip, truncateTo(reason, maxIPBlockReason), actor, truncateTo(byEmail, maxAuditEmail)).
		Scan(&out.ID, &out.IP, &out.Reason, &out.CreatedBy, &out.CreatedAt)
	if err != nil {
		return ipblock.IPBlock{}, fmt.Errorf("block ip: %w", err)
	}
	return out, nil
}

func (r *Repository) UnblockIP(ctx context.Context, ip string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM ip_block WHERE ip = $1::inet`, ipblock.Normalize(ip))
	if err != nil {
		return false, fmt.Errorf("unblock ip: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *Repository) ListIPBlocks(ctx context.Context) ([]ipblock.IPBlock, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, host(ip), reason, created_by_email, created_at
		FROM ip_block ORDER BY created_at DESC, id DESC LIMIT $1`, ipblock.MaxIPBlocks)
	if err != nil {
		return nil, fmt.Errorf("list ip blocks: %w", err)
	}
	defer rows.Close()
	out := []ipblock.IPBlock{}
	for rows.Next() {
		var b ipblock.IPBlock
		if err := rows.Scan(&b.ID, &b.IP, &b.Reason, &b.CreatedBy, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ip block: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *Repository) BlockedIPs(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT host(ip) FROM ip_block LIMIT $1`, ipblock.MaxIPBlocks)
	if err != nil {
		return nil, fmt.Errorf("blocked ips: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("scan blocked ip: %w", err)
		}
		out = append(out, ip)
	}
	return out, rows.Err()
}
