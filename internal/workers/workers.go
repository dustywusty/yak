// Package workers runs housekeeping: expired idempotency receipts and
// snapshot sessions, and event retention. Correctness never depends on these
// running on time; they only bound storage.
package workers

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dustywusty/yak/internal/domain"
)

func Run(ctx context.Context, s *domain.Service, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := Sweep(ctx, s); err != nil && ctx.Err() == nil {
			log.Warn("housekeeping sweep failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunClaimExpiry materializes lapsed claim leases (ADR-0016 §7). Reads and
// operations never depend on it (CLAIM-050); it bounds the lag between
// effective_at and recorded_at of task.claim_expired (CLAIM-054: ≤ 30 s).
func RunClaimExpiry(ctx context.Context, s *domain.Service, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.SweepClaimExpiry(ctx, 100); err != nil && ctx.Err() == nil {
			log.Warn("claim expiry sweep failed", "err", err)
		} else if n > 0 {
			log.Info("claim expiry sweep", "materialized", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func Sweep(ctx context.Context, s *domain.Service) error {
	if _, err := s.SweepPresenceExpiry(ctx, 100); err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM idempotency_receipts WHERE expires_at < now()`); err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM snapshot_sessions WHERE expires_at < now() - interval '1 hour'`); err != nil {
		return err
	}
	rows, err := s.DB.Query(ctx, `SELECT board_id FROM board_heads`)
	if err != nil {
		return err
	}
	boards, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	cutoff := s.Now().Add(-s.Cfg.EventRetention)
	for _, b := range boards {
		// Same lock order as mutations: board control row first.
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			var retained int64
			if err := tx.QueryRow(ctx, `SELECT retained_from_position FROM board_heads WHERE board_id=$1 FOR UPDATE`, b).Scan(&retained); err != nil {
				return err
			}
			var maxOld *int64
			if err := tx.QueryRow(ctx, `SELECT max(position) FROM events WHERE board_id=$1 AND recorded_at < $2`, b, cutoff).Scan(&maxOld); err != nil {
				return err
			}
			if maxOld == nil || *maxOld < retained {
				return nil
			}
			if _, err := tx.Exec(ctx, `DELETE FROM events WHERE board_id=$1 AND position <= $2`, b, *maxOld); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE board_heads SET retained_from_position=$2 WHERE board_id=$1`, b, *maxOld+1)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
