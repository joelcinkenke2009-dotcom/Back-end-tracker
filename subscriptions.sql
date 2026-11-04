-- AdPulse subscription tiers and usage quotas.
-- The application also runs an idempotent version of this migration at startup.
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
