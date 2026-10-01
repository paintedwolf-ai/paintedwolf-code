package webindex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

const providerQuotaKeyPrefix = "provider:"

// ReserveProviderQuota increments the daily counter when under cap; returns false when at cap.
func (s *Store) ReserveProviderQuota(ctx context.Context, providerID, utcDay string, cap int) (bool, error) {
	if s == nil || cap <= 0 {
		return true, nil
	}
	key := providerQuotaKey(providerID, utcDay)
	var reserved bool
	err := s.syncWrite(ctx, func(sqlDB *sql.DB) error {
		var count int
		err := sqlDB.QueryRowContext(ctx, `SELECT value FROM provider_quota WHERE key = ?`, key).Scan(&count)
		if err != nil && !db.IsNoRows(err) {
			return fmt.Errorf("read provider quota: %w", err)
		}
		if count >= cap {
			reserved = false
			return nil
		}
		if _, err := sqlDB.ExecContext(ctx, `INSERT INTO provider_quota (key, value) VALUES (?, 1)
			ON CONFLICT(key) DO UPDATE SET value = value + 1`, key); err != nil {
			return fmt.Errorf("increment provider quota: %w", err)
		}
		if err := sweepProviderQuota(ctx, sqlDB, utcDay); err != nil {
			return err
		}
		reserved = true
		return nil
	})
	return reserved, err
}

// ProviderQuotaCount reads the persisted daily call count for one provider.
func (s *Store) ProviderQuotaCount(ctx context.Context, providerID, utcDay string) (int, error) {
	if s == nil {
		return 0, nil
	}
	key := providerQuotaKey(providerID, utcDay)
	var n int
	err := s.readDB.QueryRowContext(ctx, `SELECT value FROM provider_quota WHERE key = ?`, key).Scan(&n)
	if db.IsNoRows(err) {
		return 0, nil
	}
	return n, err
}

func providerQuotaKey(providerID, utcDay string) string {
	return providerQuotaKeyPrefix + strings.TrimSpace(providerID) + ":" + strings.TrimSpace(utcDay)
}

func sweepProviderQuota(ctx context.Context, db *sql.DB, currentDay string) error {
	cutoff := strings.TrimSpace(currentDay)
	if cutoff == "" {
		cutoff = time.Now().UTC().Format("2006-01-02")
	}
	// Keys end with :YYYY-MM-DD; drop counters for prior UTC days.
	if _, err := db.ExecContext(ctx, `DELETE FROM provider_quota WHERE key LIKE ? AND length(key) >= 11 AND substr(key, length(key) - 9, 10) < ?`,
		providerQuotaKeyPrefix+"%", cutoff); err != nil {
		return fmt.Errorf("sweep provider quota: %w", err)
	}
	return nil
}
