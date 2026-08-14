package notemedia

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultSweepInterval = 15 * time.Minute

type Sweeper struct {
	pool     *pgxpool.Pool
	storage  ObjectDeleter
	logger   *slog.Logger
	interval time.Duration
	limit    int
}

func NewSweeper(pool *pgxpool.Pool, storage ObjectDeleter, logger *slog.Logger) *Sweeper {
	return &Sweeper{pool: pool, storage: storage, logger: logger, interval: defaultSweepInterval, limit: DeleteBatchMax}
}

func (s *Sweeper) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				removed, err := SystemSweepExpired(ctx, s.pool, s.storage, s.limit)
				if err != nil {
					s.logger.Warn("note media sweep failed", "err", err)
				} else if removed > 0 {
					s.logger.Info("expired note media removed", "count", removed)
				}
			}
		}
	}()
}
