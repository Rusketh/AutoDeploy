package model

import (
	"context"
	"database/sql"
	"strings"
)

// Active Directory join status values reported by the resident agent. An empty
// status means unknown / not applicable (a workgroup machine, or an image that
// doesn't join a domain).
const (
	// ADJoinJoined means the machine is domain-joined AND its secure channel
	// (trust relationship) with the domain is healthy.
	ADJoinJoined = "joined"
	// ADJoinFailed means the agent attempted an AD join and it failed.
	ADJoinFailed = "join_failed"
	// ADTrustBroken means the machine is domain-joined but the secure-channel
	// trust test fails ("the trust relationship between this workstation and
	// the primary domain failed").
	ADTrustBroken = "trust_broken"
)

// normalizeADJoinStatus maps an agent-reported status onto a known value,
// coercing anything unrecognised to "" (unknown) so a bad token can't render
// as a phantom state.
func normalizeADJoinStatus(s string) string {
	switch strings.TrimSpace(s) {
	case ADJoinJoined:
		return ADJoinJoined
	case ADJoinFailed:
		return ADJoinFailed
	case ADTrustBroken:
		return ADTrustBroken
	default:
		return ""
	}
}

// ADJoinFailure reports whether a join status represents an active failure the
// operator should see (join failed or trust broken).
func ADJoinFailure(status string) bool {
	return status == ADJoinFailed || status == ADTrustBroken
}

// ADJoinUpdate is the outcome of recording an agent's reported AD join status.
type ADJoinUpdate struct {
	Machine MachineRecord
	// NotifyEvent is non-empty only when this update TRANSITIONED into a
	// failure that should alert now — ADJoinFailed or ADTrustBroken — deduped
	// to once per failure episode. It stays empty while a failure persists
	// across polls, and the dedup markers re-arm once the machine reports a
	// healthy ADJoinJoined again.
	NotifyEvent string
}

// UpdateADJoinStatus records the agent's reported AD join status/detail for the
// machine identified by agentID, refreshing ad_join_reported_at. It returns
// whether the update transitioned into a failure that should alert now (once
// per episode): the *_notified_at markers are set when an alert fires and
// cleared when the machine reports ADJoinJoined again. detail is a
// human-readable note (an error message on failure); it must never carry a
// credential.
func (r *InventoryRepo) UpdateADJoinStatus(ctx context.Context, agentID, status, detail string) (ADJoinUpdate, error) {
	status = normalizeADJoinStatus(status)
	m, err := r.GetByAgentID(ctx, agentID)
	if err != nil {
		return ADJoinUpdate{}, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ADJoinUpdate{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var failedNotified, trustNotified sql.NullTime
	if err := tx.QueryRowContext(ctx,
		`SELECT ad_join_failed_notified_at, ad_trust_failed_notified_at
		 FROM machine_record WHERE id=?`, m.ID).Scan(&failedNotified, &trustNotified); err != nil {
		return ADJoinUpdate{}, err
	}

	notify := ""
	switch status {
	case ADJoinFailed:
		if !failedNotified.Valid {
			notify = ADJoinFailed
		}
	case ADTrustBroken:
		if !trustNotified.Valid {
			notify = ADTrustBroken
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE machine_record
		SET ad_join_status=?, ad_join_detail=?, ad_join_reported_at=CURRENT_TIMESTAMP
		WHERE id=?`, status, detail, m.ID); err != nil {
		return ADJoinUpdate{}, err
	}
	// A healthy join re-arms both alerts; a fresh failure stamps its own marker
	// so it won't re-alert until the next recovery.
	switch {
	case status == ADJoinJoined:
		if _, err := tx.ExecContext(ctx, `
			UPDATE machine_record
			SET ad_join_failed_notified_at=NULL, ad_trust_failed_notified_at=NULL
			WHERE id=?`, m.ID); err != nil {
			return ADJoinUpdate{}, err
		}
	case notify == ADJoinFailed:
		if _, err := tx.ExecContext(ctx,
			`UPDATE machine_record SET ad_join_failed_notified_at=CURRENT_TIMESTAMP WHERE id=?`, m.ID); err != nil {
			return ADJoinUpdate{}, err
		}
	case notify == ADTrustBroken:
		if _, err := tx.ExecContext(ctx,
			`UPDATE machine_record SET ad_trust_failed_notified_at=CURRENT_TIMESTAMP WHERE id=?`, m.ID); err != nil {
			return ADJoinUpdate{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ADJoinUpdate{}, err
	}

	m.ADJoinStatus = status
	m.ADJoinDetail = detail
	return ADJoinUpdate{Machine: m, NotifyEvent: notify}, nil
}
