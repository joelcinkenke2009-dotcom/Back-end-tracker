-- AdPulse subscription tiers and usage quotas.
-- The application also runs an idempotent version of this migration at startup.
-- Account tier column: 1=free, 2=$9, 3=$19, 4=$59.
-- Idempotent migration for existing registration tables.
SET @status_column_exists = (
    SELECT COUNT(*) FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'user_Tracker_Link'
      AND column_name = 'status'
);
SET @status_column_sql = IF(
    @status_column_exists = 0,
    'ALTER TABLE user_Tracker_Link ADD COLUMN status TINYINT NOT NULL DEFAULT 1',
    'SELECT 1'
);
PREPARE status_column_stmt FROM @status_column_sql;
EXECUTE status_column_stmt;
DEALLOCATE PREPARE status_column_stmt;

CREATE TABLE IF NOT EXISTS user_subscriptions (
    user_id VARCHAR(255) NOT NULL PRIMARY KEY,
    plan VARCHAR(24) NOT NULL DEFAULT 'free',
    status VARCHAR(24) NOT NULL DEFAULT 'active',
    links_limit INT NOT NULL DEFAULT 1,
    clicks_limit BIGINT NOT NULL DEFAULT 30,
    clicks_used BIGINT NOT NULL DEFAULT 0,
    analytics_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    period_started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_subscription_expiry (status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Backfill all existing accounts onto the free tier without overwriting plans.
INSERT IGNORE INTO user_subscriptions (user_id)
SELECT id FROM user_Tracker_Link WHERE id IS NOT NULL AND id <> '';

-- Preserve legacy paid users whose old is_active_date is still in the future.
-- The old database did not store which paid tier they purchased, so grant full access
-- only until their already-paid expiry; new checkouts use the exact tier mapping.
UPDATE user_subscriptions s
JOIN user_Tracker_Link u ON u.id = s.user_id
SET s.plan = 'unlimited', s.status = 'active', s.links_limit = -1,
    s.clicks_limit = -1, s.clicks_used = 0, s.analytics_enabled = TRUE,
    s.period_started_at = NOW(), s.expires_at = u.is_active_date
WHERE s.plan = 'free' AND u.is_active_date > NOW();

-- Paid plans are activated only after the payment provider confirms status=completed.
-- Each paid subscription lasts 30 days; expiration downgrades the account to free.

CREATE TABLE IF NOT EXISTS pending_subscription_payments (
    cart_id VARCHAR(255) NOT NULL PRIMARY KEY,
    user_id VARCHAR(255) NOT NULL,
    plan VARCHAR(24) NOT NULL,
    expires_at DATETIME NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_pending_subscription_user (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Synchronize account status from the authoritative subscription plan.
UPDATE user_Tracker_Link u
LEFT JOIN user_subscriptions s ON s.user_id = u.id
SET u.status = CASE
    WHEN s.plan = 'standard' THEN 2
    WHEN s.plan = 'advanced' THEN 3
    WHEN s.plan = 'unlimited' THEN 4
    ELSE 1
END;
