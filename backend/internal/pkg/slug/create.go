package slug

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const CreateMaxAttempts = 100

var ErrCreateExhausted = errors.New("could not allocate a unique slug")

type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type InsertFunc func(context.Context, pgx.Tx, string) (int64, error)
type CompleteFunc func(context.Context, pgx.Tx, int64) error

// CreateWithRetry runs each slug candidate in a fresh transaction. Complete
// shares the successful insert transaction, so dependent writes are atomic.
func CreateWithRetry(
	ctx context.Context,
	db Beginner,
	base string,
	explicit bool,
	isCollision func(error) bool,
	insert InsertFunc,
	complete CompleteFunc,
) (int64, error) {
	attempts := CreateMaxAttempts
	if explicit {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		candidate := candidateForAttempt(base, attempt+1)
		var id int64
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			var err error
			id, err = insert(ctx, tx, candidate)
			if err != nil {
				return err
			}
			if complete != nil {
				return complete(ctx, tx, id)
			}
			return nil
		})
		if err == nil {
			return id, nil
		}
		if explicit || !isCollision(err) {
			return 0, err
		}
	}
	return 0, fmt.Errorf("%w after %d attempts", ErrCreateExhausted, CreateMaxAttempts)
}
