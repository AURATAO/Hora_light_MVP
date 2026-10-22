package channel

import (
	"context"
	"database/sql"
	"fmt"
)

// PurgeUserTx deletes every chat-booking row tied to userID inside the
// caller's transaction (account deletion, server/account_deletion.go): the
// linked numbers, their conversations and setup links, and every message sent
// to or from those numbers. A phone number is personal data and goes with the
// account (D-16 R-3). Returns how many numbers were removed.
//
// A database without the channel tables (production before the D-16 go-live
// migration) is not an error: there is nothing to purge, and account deletion
// must never break because an unrelated feature has not shipped.
func PurgeUserTx(ctx context.Context, tx *sql.Tx, userID string) (int64, error) {
	var present bool
	if err := tx.QueryRowContext(ctx,
		`SELECT to_regclass('public.channel_identities') IS NOT NULL`).Scan(&present); err != nil {
		return 0, fmt.Errorf("purge chat data: probe: %w", err)
	}
	if !present {
		return 0, nil
	}
	// Children first: no FK here cascades. Messages are matched by
	// (channel, address) as well as identity_id, because outbound rows and
	// rows received before the number was linked carry only the address.
	steps := []struct{ what, q string }{
		{"setup links", `DELETE FROM public.channel_setup_tokens
			WHERE identity_id IN (SELECT id FROM public.channel_identities WHERE user_id = $1::uuid)`},
		{"conversations", `DELETE FROM public.channel_conversations
			WHERE identity_id IN (SELECT id FROM public.channel_identities WHERE user_id = $1::uuid)`},
		{"messages", `DELETE FROM public.channel_messages m
			USING public.channel_identities i
			WHERE i.user_id = $1::uuid
			  AND (m.identity_id = i.id OR (m.channel = i.channel AND m.address = i.address))`},
	}
	for _, st := range steps {
		if _, err := tx.ExecContext(ctx, st.q, userID); err != nil {
			return 0, fmt.Errorf("purge chat data: %s: %w", st.what, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM public.channel_identities WHERE user_id = $1::uuid`, userID)
	if err != nil {
		return 0, fmt.Errorf("purge chat data: numbers: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge chat data: count: %w", err)
	}
	return n, nil
}
