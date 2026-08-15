package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/silent-QAQ/redstoneapi/internal/redstone/wallet"
	"github.com/shopspring/decimal"
)

func (s *Service) RequestWithdrawal(ctx context.Context, request WithdrawalRequest) (Withdrawal, bool, error) {
	if err := s.require(); err != nil {
		return Withdrawal{}, false, err
	}
	request.PayoutMethod = strings.TrimSpace(request.PayoutMethod)
	request.PayoutReference = strings.TrimSpace(request.PayoutReference)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.UserID <= 0 || !validMoney(request.Amount) || request.FeeAmount.IsNegative() ||
		!request.FeeAmount.Equal(request.FeeAmount.Round(monetaryScale)) || !validText(request.PayoutMethod, 32) ||
		!validText(request.PayoutReference, 256) || !validText(request.IdempotencyKey, 128) {
		return Withdrawal{}, false, ErrInvalidInput
	}
	total := request.Amount.Add(request.FeeAmount)
	fingerprint := requestFingerprint("withdrawal", fmt.Sprint(request.UserID), request.Amount.StringFixed(8), request.FeeAmount.StringFixed(8), request.PayoutMethod, request.PayoutReference)
	var result Withdrawal
	created := false
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		existing, found, err := withdrawalByKey(ctx, tx, request.UserID, request.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if existingFingerprint, err := withdrawalFingerprint(ctx, tx, existing.ID); err != nil || existingFingerprint != fingerprint {
				if err != nil {
					return err
				}
				return ErrConflict
			}
			result = existing
			return nil
		}
		row := tx.QueryRowContext(ctx, `
			INSERT INTO redstone_operations_withdrawals
				(user_id, amount, fee_amount, total_debited, payout_method, payout_reference, idempotency_key, request_fingerprint)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id, created_at, updated_at
		`, request.UserID, request.Amount, request.FeeAmount, total, request.PayoutMethod, request.PayoutReference, request.IdempotencyKey, fingerprint)
		var id int64
		if err := row.Scan(&id, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return err
		}
		if _, err := s.wallet.AdjustNormalInExecutor(ctx, tx, wallet.NormalAdjustmentRequest{
			UserID: request.UserID, Delta: total.Neg(), Operation: wallet.OperationWithdrawal,
			Reference:      wallet.Reference{Type: "operations_withdrawal", ID: fmt.Sprint(id)},
			IdempotencyKey: operationKey("withdrawal", id),
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_operations_withdrawals SET status = 'pending_review', updated_at = NOW() WHERE id = $1
		`, id); err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, request.UserID, "withdrawal", fmt.Sprint(id), "withdrawal_requested", "{}"); err != nil {
			return err
		}
		result = Withdrawal{ID: id, UserID: request.UserID, Amount: request.Amount, FeeAmount: request.FeeAmount, TotalDebited: total, PayoutMethod: request.PayoutMethod, Status: "pending_review", CreatedAt: result.CreatedAt, UpdatedAt: result.UpdatedAt}
		created = true
		return nil
	})
	return result, created, err
}

func (s *Service) ListWithdrawals(ctx context.Context, userID int64, limit, offset int) ([]Withdrawal, int, error) {
	if err := s.require(); err != nil {
		return nil, 0, err
	}
	if userID <= 0 || limit <= 0 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidInput
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_operations_withdrawals WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, withdrawalSelect+` WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, _, err := scanWithdrawals(rows)
	return items, total, err
}

func (s *Service) ListWithdrawalQueue(ctx context.Context, limit, offset int) ([]Withdrawal, int, error) {
	if err := s.require(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidInput
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_operations_withdrawals WHERE status IN ('pending_review', 'approved')`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, withdrawalSelect+` WHERE status IN ('pending_review', 'approved') ORDER BY created_at ASC, id ASC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, _, err := scanWithdrawals(rows)
	return items, total, err
}

func (s *Service) ResolveWithdrawal(ctx context.Context, adminUserID, withdrawalID int64, action, note string) (Withdrawal, error) {
	if err := s.require(); err != nil {
		return Withdrawal{}, err
	}
	action, note = strings.TrimSpace(action), strings.TrimSpace(note)
	if adminUserID <= 0 || withdrawalID <= 0 || !validText(action, 16) || len(note) > 1000 || (action != "approve" && action != "pay" && action != "reject") {
		return Withdrawal{}, ErrInvalidInput
	}
	var result Withdrawal
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		withdrawal, found, err := withdrawalByID(ctx, tx, withdrawalID, true)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		switch action {
		case "approve":
			if withdrawal.Status == "approved" {
				result = withdrawal
				return nil
			}
			if withdrawal.Status != "pending_review" {
				return ErrConflict
			}
			withdrawal.Status = "approved"
		case "pay":
			if withdrawal.Status == "paid" {
				result = withdrawal
				return nil
			}
			if withdrawal.Status != "approved" {
				return ErrConflict
			}
			withdrawal.Status = "paid"
		case "reject":
			if withdrawal.Status == "rejected" {
				result = withdrawal
				return nil
			}
			if withdrawal.Status != "pending_review" && withdrawal.Status != "approved" {
				return ErrConflict
			}
			if _, err := s.wallet.AdjustNormalInExecutor(ctx, tx, wallet.NormalAdjustmentRequest{
				UserID: withdrawal.UserID, Delta: withdrawal.TotalDebited, Operation: wallet.OperationRefund,
				Reference:      wallet.Reference{Type: "operations_withdrawal", ID: fmt.Sprint(withdrawalID)},
				IdempotencyKey: operationKey("withdrawal-refund", withdrawalID),
			}); err != nil {
				return err
			}
			withdrawal.Status = "rejected"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_operations_withdrawals
			SET status = $1, admin_note = $2, processed_by_user_id = $3, processed_at = NOW(), updated_at = NOW()
			WHERE id = $4
		`, withdrawal.Status, note, adminUserID, withdrawalID); err != nil {
			return err
		}
		if err := writeAudit(ctx, tx, adminUserID, "withdrawal", fmt.Sprint(withdrawalID), "withdrawal_"+action, "{}"); err != nil {
			return err
		}
		withdrawal.AdminNote = note
		withdrawal.ProcessedByUserID = &adminUserID
		withdrawal.ProcessedAt = timePtr(time.Now().UTC())
		result = withdrawal
		return nil
	})
	return result, err
}

// AdjustNormal writes one compensation-style ordinary-balance entry. It does
// not expose the legacy mutable balance setter and rejects normal overdrafts.
func (s *Service) AdjustNormal(ctx context.Context, adminUserID, userID int64, delta decimal.Decimal, referenceID, note string) (wallet.CreditResult, error) {
	if err := s.require(); err != nil {
		return wallet.CreditResult{}, err
	}
	if adminUserID <= 0 || userID <= 0 || !validMoney(delta.Abs()) || !validText(referenceID, 128) || len(strings.TrimSpace(note)) > 1000 {
		return wallet.CreditResult{}, ErrInvalidInput
	}
	var result wallet.CreditResult
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		result, err = s.wallet.AdjustNormalInExecutor(ctx, tx, wallet.NormalAdjustmentRequest{
			UserID: userID, Delta: delta, Operation: wallet.OperationAdminAdjustment,
			Reference:      wallet.Reference{Type: "operations_admin_adjustment", ID: referenceID},
			IdempotencyKey: "operations-admin-adjustment-" + referenceID,
		})
		if err != nil {
			return err
		}
		return writeAudit(ctx, tx, adminUserID, "wallet", referenceID, "normal_balance_adjusted", fmt.Sprintf(`{"user_id":%d}`, userID))
	})
	return result, err
}

func (s *Service) GrantReferralReward(ctx context.Context, adminUserID, inviterUserID, invitedUserID int64, sourceType, sourceID string, amount decimal.Decimal) error {
	if err := s.require(); err != nil {
		return err
	}
	sourceType, sourceID = strings.TrimSpace(sourceType), strings.TrimSpace(sourceID)
	if adminUserID <= 0 || inviterUserID <= 0 || invitedUserID <= 0 || inviterUserID == invitedUserID || !validText(sourceType, 64) || !validText(sourceID, 128) || !validMoney(amount) {
		return ErrInvalidInput
	}
	return withTx(ctx, s.db, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO redstone_operations_referral_rewards
				(inviter_user_id, invited_user_id, source_type, source_id, amount, wallet_operation_key, granted_by_user_id)
			VALUES ($1, $2, $3, $4, $5, 'pending', $6)
			ON CONFLICT (inviter_user_id, source_type, source_id) DO NOTHING
			RETURNING id
		`, inviterUserID, invitedUserID, sourceType, sourceID, amount, adminUserID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		key := operationKey("referral", id)
		if _, err := tx.ExecContext(ctx, `UPDATE redstone_operations_referral_rewards SET wallet_operation_key = $1 WHERE id = $2`, key, id); err != nil {
			return err
		}
		if _, err := s.wallet.CreditInExecutor(ctx, tx, wallet.CreditRequest{
			UserID: inviterUserID, Asset: wallet.AssetNormal, Amount: amount, Reason: wallet.CreditReferralReward,
			Reference: wallet.Reference{Type: "operations_referral", ID: fmt.Sprint(id)}, IdempotencyKey: key,
		}); err != nil {
			return err
		}
		return writeAudit(ctx, tx, adminUserID, "referral_reward", fmt.Sprint(id), "referral_reward_granted", "{}")
	})
}

func (s *Service) CreateInvoiceProfile(ctx context.Context, request InvoiceProfileRequest) (InvoiceProfile, error) {
	if err := s.require(); err != nil {
		return InvoiceProfile{}, err
	}
	request.InvoiceType, request.TitleName, request.TaxID, request.RecipientEmail = strings.TrimSpace(request.InvoiceType), strings.TrimSpace(request.TitleName), strings.TrimSpace(request.TaxID), strings.TrimSpace(request.RecipientEmail)
	if request.UserID <= 0 || !validText(request.TitleName, 255) || !validText(request.RecipientEmail, 255) ||
		(request.InvoiceType != "personal_normal" && request.InvoiceType != "enterprise_normal" && request.InvoiceType != "enterprise_special") || len(request.TaxID) > 64 {
		return InvoiceProfile{}, ErrInvalidInput
	}
	var profile InvoiceProfile
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		if request.IsDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE redstone_operations_invoice_profiles SET is_default = FALSE, updated_at = NOW() WHERE user_id = $1 AND is_default`, request.UserID); err != nil {
				return err
			}
		}
		return tx.QueryRowContext(ctx, `
			INSERT INTO redstone_operations_invoice_profiles (user_id, invoice_type, title_name, tax_id, recipient_email, is_default)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, user_id, invoice_type, title_name, tax_id, recipient_email, is_default, created_at, updated_at
		`, request.UserID, request.InvoiceType, request.TitleName, request.TaxID, request.RecipientEmail, request.IsDefault).Scan(
			&profile.ID, &profile.UserID, &profile.InvoiceType, &profile.TitleName, &profile.TaxID, &profile.RecipientEmail, &profile.IsDefault, &profile.CreatedAt, &profile.UpdatedAt,
		)
	})
	return profile, err
}

func (s *Service) ListInvoiceProfiles(ctx context.Context, userID int64) ([]InvoiceProfile, error) {
	if err := s.require(); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, invoice_type, title_name, tax_id, recipient_email, is_default, created_at, updated_at
		FROM redstone_operations_invoice_profiles WHERE user_id = $1 ORDER BY is_default DESC, id DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]InvoiceProfile, 0)
	for rows.Next() {
		var profile InvoiceProfile
		if err := rows.Scan(&profile.ID, &profile.UserID, &profile.InvoiceType, &profile.TitleName, &profile.TaxID, &profile.RecipientEmail, &profile.IsDefault, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// RequestInvoice derives the amount from the immutable payment ledger. A user
// cannot submit an arbitrary invoice amount or invoice another user's payment.
func (s *Service) RequestInvoice(ctx context.Context, request InvoiceRequestInput) (InvoiceRequest, bool, error) {
	if err := s.require(); err != nil {
		return InvoiceRequest{}, false, err
	}
	request.PaymentRefID = strings.TrimSpace(request.PaymentRefID)
	if request.UserID <= 0 || request.ProfileID <= 0 || !validText(request.PaymentRefID, 128) {
		return InvoiceRequest{}, false, ErrInvalidInput
	}
	var result InvoiceRequest
	created := false
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		var profileUserID int64
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM redstone_operations_invoice_profiles WHERE id = $1 FOR KEY SHARE`, request.ProfileID).Scan(&profileUserID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if profileUserID != request.UserID {
			return ErrForbidden
		}
		var amount decimal.Decimal
		if err := tx.QueryRowContext(ctx, `
			SELECT delta FROM redstone_wallet_ledger
			WHERE user_id = $1 AND asset_type = 'normal' AND operation = 'payment'
				AND reference_type = 'payment_order' AND reference_id = $2 AND delta > 0
			ORDER BY id DESC LIMIT 1
		`, request.UserID, request.PaymentRefID).Scan(&amount); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		number := fmt.Sprintf("INV-%d-%s", time.Now().UTC().UnixNano(), request.PaymentRefID)
		err := tx.QueryRowContext(ctx, `
			INSERT INTO redstone_operations_invoice_requests
				(request_number, user_id, profile_id, amount, source_type, source_id)
			VALUES ($1, $2, $3, $4, 'wallet_payment', $5)
			ON CONFLICT (user_id, source_type, source_id) DO NOTHING
			RETURNING id, request_number, user_id, profile_id, amount, currency, source_type, source_id, status, invoice_number, file_reference, note, created_at, updated_at
		`, number, request.UserID, request.ProfileID, amount, request.PaymentRefID).Scan(
			&result.ID, &result.RequestNumber, &result.UserID, &result.ProfileID, &result.Amount, &result.Currency, &result.SourceType, &result.SourceID, &result.Status, &result.InvoiceNumber, &result.FileReference, &result.Note, &result.CreatedAt, &result.UpdatedAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			var found bool
			result, found, err = invoiceBySource(ctx, tx, request.UserID, "wallet_payment", request.PaymentRefID)
			if err != nil {
				return err
			}
			if !found {
				return ErrConflict
			}
			return nil
		}
		created = true
		return err
	})
	return result, created, err
}

func (s *Service) ListInvoices(ctx context.Context, userID int64, limit, offset int) ([]InvoiceRequest, int, error) {
	if err := s.require(); err != nil {
		return nil, 0, err
	}
	if userID <= 0 || limit <= 0 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidInput
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_operations_invoice_requests WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, invoiceSelect+` WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanInvoices(rows)
	return items, total, err
}

func (s *Service) ResolveInvoice(ctx context.Context, adminUserID, invoiceID int64, status, invoiceNumber, fileReference, note string) (InvoiceRequest, error) {
	if err := s.require(); err != nil {
		return InvoiceRequest{}, err
	}
	status, invoiceNumber, fileReference, note = strings.TrimSpace(status), strings.TrimSpace(invoiceNumber), strings.TrimSpace(fileReference), strings.TrimSpace(note)
	if adminUserID <= 0 || invoiceID <= 0 || (status != "issued" && status != "rejected") || len(invoiceNumber) > 128 || len(fileReference) > 256 || len(note) > 1000 {
		return InvoiceRequest{}, ErrInvalidInput
	}
	if status == "issued" && !validText(invoiceNumber, 128) {
		return InvoiceRequest{}, ErrInvalidInput
	}
	var result InvoiceRequest
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		item, found, err := invoiceByID(ctx, tx, invoiceID, true)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if item.Status == status {
			result = item
			return nil
		}
		if item.Status != "pending" {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_operations_invoice_requests
			SET status = $1, invoice_number = $2, file_reference = $3, note = $4, processed_by_user_id = $5, processed_at = NOW(), updated_at = NOW()
			WHERE id = $6
		`, status, invoiceNumber, fileReference, note, adminUserID, invoiceID); err != nil {
			return err
		}
		item.Status, item.InvoiceNumber, item.FileReference, item.Note = status, invoiceNumber, fileReference, note
		result = item
		return writeAudit(ctx, tx, adminUserID, "invoice", fmt.Sprint(invoiceID), "invoice_"+status, "{}")
	})
	return result, err
}

func (s *Service) CreateTicket(ctx context.Context, request TicketRequest) (Ticket, error) {
	if err := s.require(); err != nil {
		return Ticket{}, err
	}
	request.Subject, request.Category, request.Body = strings.TrimSpace(request.Subject), strings.TrimSpace(request.Category), strings.TrimSpace(request.Body)
	if request.UserID <= 0 || !validText(request.Subject, 200) || !validText(request.Body, 8000) || len(request.Category) > 32 {
		return Ticket{}, ErrInvalidInput
	}
	if request.Category == "" {
		request.Category = "support"
	}
	var ticket Ticket
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO redstone_operations_tickets (user_id, subject, category)
			VALUES ($1, $2, $3)
			RETURNING id, user_id, subject, category, status, priority, assigned_admin_id, last_message_at, created_at, updated_at
		`, request.UserID, request.Subject, request.Category).Scan(
			&ticket.ID, &ticket.UserID, &ticket.Subject, &ticket.Category, &ticket.Status, &ticket.Priority, &ticket.AssignedAdminID, &ticket.LastMessageAt, &ticket.CreatedAt, &ticket.UpdatedAt,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO redstone_operations_ticket_messages (ticket_id, sender_kind, sender_user_id, body) VALUES ($1, 'user', $2, $3)
		`, ticket.ID, request.UserID, request.Body)
		return err
	})
	return ticket, err
}

func (s *Service) ListTickets(ctx context.Context, userID int64, limit, offset int) ([]Ticket, int, error) {
	if err := s.require(); err != nil {
		return nil, 0, err
	}
	if userID <= 0 || limit <= 0 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidInput
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_operations_tickets WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, ticketSelect+` WHERE user_id = $1 ORDER BY last_message_at DESC, id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanTickets(rows)
	return items, total, err
}

func (s *Service) ListTicketQueue(ctx context.Context, limit, offset int) ([]Ticket, int, error) {
	if err := s.require(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidInput
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_operations_tickets WHERE status IN ('open', 'pending_admin')`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, ticketSelect+` WHERE status IN ('open', 'pending_admin') ORDER BY priority DESC, last_message_at ASC, id ASC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items, err := scanTickets(rows)
	return items, total, err
}

func (s *Service) ListTicketMessages(ctx context.Context, userID, ticketID int64, admin bool) ([]TicketMessage, error) {
	if err := s.require(); err != nil {
		return nil, err
	}
	if userID <= 0 || ticketID <= 0 {
		return nil, ErrInvalidInput
	}
	query := `SELECT id FROM redstone_operations_tickets WHERE id = $1`
	args := []any{ticketID}
	if !admin {
		query += ` AND user_id = $2`
		args = append(args, userID)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrForbidden
	} else if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ticket_id, sender_kind, sender_user_id, body, created_at
		FROM redstone_operations_ticket_messages WHERE ticket_id = $1 ORDER BY id ASC
	`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]TicketMessage, 0)
	for rows.Next() {
		var item TicketMessage
		if err := rows.Scan(&item.ID, &item.TicketID, &item.SenderKind, &item.SenderUserID, &item.Body, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) ReplyTicket(ctx context.Context, actorUserID, ticketID int64, admin bool, body string) (Ticket, error) {
	if err := s.require(); err != nil {
		return Ticket{}, err
	}
	body = strings.TrimSpace(body)
	if actorUserID <= 0 || ticketID <= 0 || !validText(body, 8000) {
		return Ticket{}, ErrInvalidInput
	}
	var ticket Ticket
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		current, found, err := ticketByID(ctx, tx, ticketID, true)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if !admin && current.UserID != actorUserID {
			return ErrForbidden
		}
		if current.Status == "closed" {
			return ErrConflict
		}
		sender := "user"
		nextStatus := "pending_admin"
		if admin {
			sender, nextStatus = "admin", "pending_user"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO redstone_operations_ticket_messages (ticket_id, sender_kind, sender_user_id, body) VALUES ($1, $2, $3, $4)`, ticketID, sender, actorUserID, body); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE redstone_operations_tickets SET status = $1, last_message_at = NOW(), updated_at = NOW() WHERE id = $2`, nextStatus, ticketID); err != nil {
			return err
		}
		current.Status = nextStatus
		ticket = current
		return nil
	})
	return ticket, err
}

func (s *Service) ResolveTicket(ctx context.Context, adminUserID, ticketID int64, status, priority string, assignee *int64) (Ticket, error) {
	if err := s.require(); err != nil {
		return Ticket{}, err
	}
	status, priority = strings.TrimSpace(status), strings.TrimSpace(priority)
	if adminUserID <= 0 || ticketID <= 0 || (status != "open" && status != "resolved" && status != "closed") || (priority != "low" && priority != "normal" && priority != "high" && priority != "urgent") {
		return Ticket{}, ErrInvalidInput
	}
	var ticket Ticket
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		current, found, err := ticketByID(ctx, tx, ticketID, true)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE redstone_operations_tickets SET status = $1, priority = $2, assigned_admin_id = $3, updated_at = NOW() WHERE id = $4
		`, status, priority, assignee, ticketID); err != nil {
			return err
		}
		current.Status, current.Priority, current.AssignedAdminID = status, priority, assignee
		ticket = current
		return writeAudit(ctx, tx, adminUserID, "ticket", fmt.Sprint(ticketID), "ticket_"+status, "{}")
	})
	return ticket, err
}

func (s *Service) CreateCampaign(ctx context.Context, request CampaignRequest) (Campaign, error) {
	if err := s.require(); err != nil {
		return Campaign{}, err
	}
	request.Name, request.Description = strings.TrimSpace(request.Name), strings.TrimSpace(request.Description)
	if request.AdminUserID <= 0 || !validText(request.Name, 120) || len(request.Description) > 2000 ||
		!request.StartsAt.Before(request.EndsAt) || !validMoney(request.RewardAmount) || request.MaxClaims < 0 {
		return Campaign{}, ErrInvalidInput
	}
	var campaign Campaign
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO redstone_operations_campaigns
			(name, description, starts_at, ends_at, reward_amount, max_claims, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, name, description, status, starts_at, ends_at, reward_amount, max_claims, created_at, updated_at
	`, request.Name, request.Description, request.StartsAt.UTC(), request.EndsAt.UTC(), request.RewardAmount, request.MaxClaims, request.AdminUserID).Scan(
		&campaign.ID, &campaign.Name, &campaign.Description, &campaign.Status, &campaign.StartsAt, &campaign.EndsAt, &campaign.RewardAmount, &campaign.MaxClaims, &campaign.CreatedAt, &campaign.UpdatedAt,
	)
	return campaign, err
}

func (s *Service) SetCampaignStatus(ctx context.Context, adminUserID, campaignID int64, status string) (Campaign, error) {
	if err := s.require(); err != nil {
		return Campaign{}, err
	}
	status = strings.TrimSpace(status)
	if adminUserID <= 0 || campaignID <= 0 || (status != "draft" && status != "active" && status != "paused" && status != "ended") {
		return Campaign{}, ErrInvalidInput
	}
	var campaign Campaign
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			UPDATE redstone_operations_campaigns SET status = $1, updated_at = NOW() WHERE id = $2
			RETURNING id, name, description, status, starts_at, ends_at, reward_amount, max_claims, created_at, updated_at
		`, status, campaignID).Scan(&campaign.ID, &campaign.Name, &campaign.Description, &campaign.Status, &campaign.StartsAt, &campaign.EndsAt, &campaign.RewardAmount, &campaign.MaxClaims, &campaign.CreatedAt, &campaign.UpdatedAt); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return writeAudit(ctx, tx, adminUserID, "campaign", fmt.Sprint(campaignID), "campaign_"+status, "{}")
	})
	return campaign, err
}

func (s *Service) ListActiveCampaigns(ctx context.Context) ([]Campaign, error) {
	if err := s.require(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, campaignSelect+` WHERE status = 'active' AND starts_at <= NOW() AND ends_at > NOW() ORDER BY starts_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Campaign, 0)
	for rows.Next() {
		var campaign Campaign
		if err := rows.Scan(&campaign.ID, &campaign.Name, &campaign.Description, &campaign.Status, &campaign.StartsAt, &campaign.EndsAt, &campaign.RewardAmount, &campaign.MaxClaims, &campaign.CreatedAt, &campaign.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, campaign)
	}
	return items, rows.Err()
}

func (s *Service) ClaimCampaign(ctx context.Context, userID, campaignID int64) (decimal.Decimal, bool, error) {
	if err := s.require(); err != nil {
		return decimal.Zero, false, err
	}
	if userID <= 0 || campaignID <= 0 {
		return decimal.Zero, false, ErrInvalidInput
	}
	var amount decimal.Decimal
	created := false
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		var campaign Campaign
		if err := tx.QueryRowContext(ctx, campaignSelect+` WHERE id = $1 FOR UPDATE`, campaignID).Scan(
			&campaign.ID, &campaign.Name, &campaign.Description, &campaign.Status, &campaign.StartsAt, &campaign.EndsAt, &campaign.RewardAmount, &campaign.MaxClaims, &campaign.CreatedAt, &campaign.UpdatedAt,
		); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		now := time.Now().UTC()
		if campaign.Status != "active" || now.Before(campaign.StartsAt) || !now.Before(campaign.EndsAt) {
			return ErrCampaignUnavailable
		}
		var claimID int64
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT id, amount, status FROM redstone_operations_campaign_claims WHERE campaign_id = $1 AND user_id = $2 FOR UPDATE`, campaignID, userID).Scan(&claimID, &amount, &status); err == nil {
			if status == "granted" {
				return nil
			}
			return ErrConflict
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if campaign.MaxClaims > 0 {
			var used int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM redstone_operations_campaign_claims WHERE campaign_id = $1 AND status IN ('pending', 'granted')`, campaignID).Scan(&used); err != nil {
				return err
			}
			if used >= campaign.MaxClaims {
				return ErrCampaignUnavailable
			}
		}
		amount = campaign.RewardAmount
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO redstone_operations_campaign_claims (campaign_id, user_id, amount, wallet_operation_key)
			VALUES ($1, $2, $3, 'pending') RETURNING id
		`, campaignID, userID, amount).Scan(&claimID); err != nil {
			return err
		}
		key := operationKey("campaign-claim", claimID)
		if _, err := tx.ExecContext(ctx, `UPDATE redstone_operations_campaign_claims SET wallet_operation_key = $1 WHERE id = $2`, key, claimID); err != nil {
			return err
		}
		if _, err := s.wallet.CreditInExecutor(ctx, tx, wallet.CreditRequest{
			UserID: userID, Asset: wallet.AssetNormal, Amount: amount, Reason: wallet.CreditActivityReward,
			Reference: wallet.Reference{Type: "operations_campaign_claim", ID: fmt.Sprint(claimID)}, IdempotencyKey: key,
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE redstone_operations_campaign_claims SET status = 'granted', granted_at = NOW() WHERE id = $1`, claimID); err != nil {
			return err
		}
		created = true
		return nil
	})
	return amount, created, err
}

func (s *Service) ReportContent(ctx context.Context, reporterUserID int64, subjectType, subjectID, reason string) (ContentCase, bool, error) {
	if err := s.require(); err != nil {
		return ContentCase{}, false, err
	}
	subjectType, subjectID, reason = strings.TrimSpace(subjectType), strings.TrimSpace(subjectID), strings.TrimSpace(reason)
	if reporterUserID <= 0 || !validText(subjectType, 64) || !validText(subjectID, 128) || !validText(reason, 1000) {
		return ContentCase{}, false, ErrInvalidInput
	}
	var item ContentCase
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO redstone_operations_content_cases (reporter_user_id, subject_type, subject_id, reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (reporter_user_id, subject_type, subject_id) DO NOTHING
		RETURNING id, reporter_user_id, subject_type, subject_id, reason, status, decision_note, decided_by_user_id, decided_at, created_at, updated_at
	`, reporterUserID, subjectType, subjectID, reason).Scan(
		&item.ID, &item.ReporterUserID, &item.SubjectType, &item.SubjectID, &item.Reason, &item.Status, &item.DecisionNote, &item.DecidedByUserID, &item.DecidedAt, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, contentCaseSelect+` WHERE reporter_user_id = $1 AND subject_type = $2 AND subject_id = $3`, reporterUserID, subjectType, subjectID).Scan(
			&item.ID, &item.ReporterUserID, &item.SubjectType, &item.SubjectID, &item.Reason, &item.Status, &item.DecisionNote, &item.DecidedByUserID, &item.DecidedAt, &item.CreatedAt, &item.UpdatedAt,
		)
		return item, false, err
	}
	return item, err == nil, err
}

func (s *Service) ResolveContentCase(ctx context.Context, adminUserID, caseID int64, status, note string) (ContentCase, error) {
	if err := s.require(); err != nil {
		return ContentCase{}, err
	}
	status, note = strings.TrimSpace(status), strings.TrimSpace(note)
	if adminUserID <= 0 || caseID <= 0 || (status != "dismissed" && status != "restricted" && status != "removed") || len(note) > 1000 {
		return ContentCase{}, ErrInvalidInput
	}
	var item ContentCase
	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			UPDATE redstone_operations_content_cases
			SET status = $1, decision_note = $2, decided_by_user_id = $3, decided_at = NOW(), updated_at = NOW()
			WHERE id = $4 AND status = 'open'
			RETURNING id, reporter_user_id, subject_type, subject_id, reason, status, decision_note, decided_by_user_id, decided_at, created_at, updated_at
		`, status, note, adminUserID, caseID).Scan(
			&item.ID, &item.ReporterUserID, &item.SubjectType, &item.SubjectID, &item.Reason, &item.Status, &item.DecisionNote, &item.DecidedByUserID, &item.DecidedAt, &item.CreatedAt, &item.UpdatedAt,
		); errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		} else if err != nil {
			return err
		}
		return writeAudit(ctx, tx, adminUserID, "content_case", fmt.Sprint(caseID), "content_"+status, "{}")
	})
	return item, err
}

func (s *Service) ListAvailableProxies(ctx context.Context, userID int64) ([]ProxyOption, error) {
	if err := s.require(); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.name, p.protocol, p.host, p.port, p.owner_user_id, p.max_accounts, COUNT(a.id) AS account_count
		FROM proxies p
		LEFT JOIN accounts a ON a.proxy_id = p.id AND a.deleted_at IS NULL
		WHERE p.status = 'active' AND (p.owner_user_id IS NULL OR p.owner_user_id = $1)
		GROUP BY p.id
		HAVING p.max_accounts = 0 OR COUNT(a.id) < p.max_accounts
		ORDER BY p.name ASC, p.id ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProxyOption, 0)
	for rows.Next() {
		var item ProxyOption
		if err := rows.Scan(&item.ID, &item.Name, &item.Protocol, &item.Host, &item.Port, &item.OwnerUserID, &item.MaxAccounts, &item.AccountCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) AssignProxy(ctx context.Context, adminUserID, proxyID int64, ownerUserID *int64, maxAccounts int) error {
	if err := s.require(); err != nil {
		return err
	}
	if adminUserID <= 0 || proxyID <= 0 || maxAccounts < 0 || (ownerUserID != nil && *ownerUserID <= 0) {
		return ErrInvalidInput
	}
	return withTx(ctx, s.db, func(tx *sql.Tx) error {
		var currentOwner *int64
		if err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM proxies WHERE id = $1 FOR UPDATE`, proxyID).Scan(&currentOwner); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		_ = currentOwner
		if ownerUserID != nil {
			var foreignCount int
			if err := tx.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM accounts WHERE proxy_id = $1 AND deleted_at IS NULL AND (owner_user_id IS NULL OR owner_user_id <> $2)
			`, proxyID, *ownerUserID).Scan(&foreignCount); err != nil {
				return err
			}
			if foreignCount > 0 {
				return ErrConflict
			}
		}
		var boundCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE proxy_id = $1 AND deleted_at IS NULL`, proxyID).Scan(&boundCount); err != nil {
			return err
		}
		if maxAccounts > 0 && boundCount > maxAccounts {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxies SET owner_user_id = $1, max_accounts = $2, updated_at = NOW() WHERE id = $3`, ownerUserID, maxAccounts, proxyID); err != nil {
			return err
		}
		return writeAudit(ctx, tx, adminUserID, "proxy", fmt.Sprint(proxyID), "proxy_assignment_updated", "{}")
	})
}

const withdrawalSelect = `
	SELECT id, user_id, amount, fee_amount, total_debited, payout_method, status, admin_note, processed_by_user_id, processed_at, created_at, updated_at
	FROM redstone_operations_withdrawals`
const invoiceSelect = `
	SELECT id, request_number, user_id, profile_id, amount, currency, source_type, source_id, status, invoice_number, file_reference, note, created_at, updated_at
	FROM redstone_operations_invoice_requests`
const ticketSelect = `
	SELECT id, user_id, subject, category, status, priority, assigned_admin_id, last_message_at, created_at, updated_at
	FROM redstone_operations_tickets`
const campaignSelect = `
	SELECT id, name, description, status, starts_at, ends_at, reward_amount, max_claims, created_at, updated_at
	FROM redstone_operations_campaigns`
const contentCaseSelect = `
	SELECT id, reporter_user_id, subject_type, subject_id, reason, status, decision_note, decided_by_user_id, decided_at, created_at, updated_at
	FROM redstone_operations_content_cases`

func withdrawalByKey(ctx context.Context, tx *sql.Tx, userID int64, key string) (Withdrawal, bool, error) {
	row := tx.QueryRowContext(ctx, withdrawalSelect+` WHERE user_id = $1 AND idempotency_key = $2 FOR UPDATE`, userID, key)
	return scanWithdrawal(row)
}
func withdrawalByID(ctx context.Context, tx *sql.Tx, id int64, lock bool) (Withdrawal, bool, error) {
	query := withdrawalSelect + ` WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanWithdrawal(tx.QueryRowContext(ctx, query, id))
}
func withdrawalFingerprint(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var value string
	return value, tx.QueryRowContext(ctx, `SELECT request_fingerprint FROM redstone_operations_withdrawals WHERE id = $1`, id).Scan(&value)
}
func scanWithdrawal(row *sql.Row) (Withdrawal, bool, error) {
	var item Withdrawal
	err := row.Scan(&item.ID, &item.UserID, &item.Amount, &item.FeeAmount, &item.TotalDebited, &item.PayoutMethod, &item.Status, &item.AdminNote, &item.ProcessedByUserID, &item.ProcessedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Withdrawal{}, false, nil
	}
	return item, err == nil, err
}
func scanWithdrawals(rows *sql.Rows) ([]Withdrawal, int, error) {
	items := make([]Withdrawal, 0)
	for rows.Next() {
		var item Withdrawal
		if err := rows.Scan(&item.ID, &item.UserID, &item.Amount, &item.FeeAmount, &item.TotalDebited, &item.PayoutMethod, &item.Status, &item.AdminNote, &item.ProcessedByUserID, &item.ProcessedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, len(items), rows.Err()
}
func invoiceBySource(ctx context.Context, tx *sql.Tx, userID int64, sourceType, sourceID string) (InvoiceRequest, bool, error) {
	return scanInvoice(tx.QueryRowContext(ctx, invoiceSelect+` WHERE user_id = $1 AND source_type = $2 AND source_id = $3`, userID, sourceType, sourceID))
}
func invoiceByID(ctx context.Context, tx *sql.Tx, id int64, lock bool) (InvoiceRequest, bool, error) {
	query := invoiceSelect + ` WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanInvoice(tx.QueryRowContext(ctx, query, id))
}
func scanInvoice(row *sql.Row) (InvoiceRequest, bool, error) {
	var item InvoiceRequest
	err := row.Scan(&item.ID, &item.RequestNumber, &item.UserID, &item.ProfileID, &item.Amount, &item.Currency, &item.SourceType, &item.SourceID, &item.Status, &item.InvoiceNumber, &item.FileReference, &item.Note, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InvoiceRequest{}, false, nil
	}
	return item, err == nil, err
}
func scanInvoices(rows *sql.Rows) ([]InvoiceRequest, error) {
	items := make([]InvoiceRequest, 0)
	for rows.Next() {
		var item InvoiceRequest
		if err := rows.Scan(&item.ID, &item.RequestNumber, &item.UserID, &item.ProfileID, &item.Amount, &item.Currency, &item.SourceType, &item.SourceID, &item.Status, &item.InvoiceNumber, &item.FileReference, &item.Note, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func ticketByID(ctx context.Context, tx *sql.Tx, id int64, lock bool) (Ticket, bool, error) {
	query := ticketSelect + ` WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	row := tx.QueryRowContext(ctx, query, id)
	var item Ticket
	err := row.Scan(&item.ID, &item.UserID, &item.Subject, &item.Category, &item.Status, &item.Priority, &item.AssignedAdminID, &item.LastMessageAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, false, nil
	}
	return item, err == nil, err
}
func scanTickets(rows *sql.Rows) ([]Ticket, error) {
	items := make([]Ticket, 0)
	for rows.Next() {
		var item Ticket
		if err := rows.Scan(&item.ID, &item.UserID, &item.Subject, &item.Category, &item.Status, &item.Priority, &item.AssignedAdminID, &item.LastMessageAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func writeAudit(ctx context.Context, tx *sql.Tx, actorID int64, subjectType, subjectID, action, detail string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO redstone_operations_audits (actor_user_id, subject_type, subject_id, action, detail) VALUES ($1, $2, $3, $4, $5::jsonb)`, actorID, subjectType, subjectID, action, detail)
	return err
}
func timePtr(value time.Time) *time.Time { return &value }
