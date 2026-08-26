package adminconsole

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	ErrInvalidCursor = errors.New("invalid cursor")
	ErrInvalidQuery  = errors.New("invalid query")
	ErrNotFound      = errors.New("account not found")
)

type MutationServices struct {
	Enable   func(context.Context, string, int64, string) error
	Disable  func(context.Context, string, int64, string) error
	SetQuota func(context.Context, string, int64, uint64, string) error
	Room     RoomMutation
}

// RoomMutation is the single administrator room-change interface. Success
// means the canonical target was persisted through runtime ownership fencing
// and the durable enabled-account reference snapshot was synchronized.
type RoomMutation interface {
	SetRoom(context.Context, int64, string) (RoomMutationResult, error)
}

type RoomMutationResult struct {
	OldCanonical string
	NewCanonical string
}

type Service struct {
	db           *sql.DB
	publicOrigin string
	mutations    MutationServices
}

func NewService(db *sql.DB, publicOrigin string, mutations ...MutationServices) (*Service, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	service := &Service{db: db, publicOrigin: strings.TrimRight(publicOrigin, "/")}
	if len(mutations) > 0 {
		service.mutations = mutations[0]
	}
	return service, nil
}

func (service *Service) Batch(ctx context.Context, token string, request BatchRequest) (BatchResponse, error) {
	if service == nil || token == "" || len(request.AccountIDs) < 1 || len(request.AccountIDs) > 100 || !validReason(request.Reason) {
		return BatchResponse{}, ErrInvalidQuery
	}
	request.Reason = strings.TrimSpace(request.Reason)
	seen := map[int64]struct{}{}
	for _, id := range request.AccountIDs {
		if id <= 0 {
			return BatchResponse{}, ErrInvalidQuery
		}
		if _, ok := seen[id]; ok {
			return BatchResponse{}, ErrInvalidQuery
		}
		seen[id] = struct{}{}
	}
	if request.Action != BatchEnable && request.Action != BatchDisable && request.Action != BatchQuota {
		return BatchResponse{}, ErrInvalidQuery
	}
	if request.Action == BatchQuota && (request.RemainingQuota == nil || *request.RemainingQuota < 0) {
		return BatchResponse{}, ErrInvalidQuery
	}
	if request.Action != BatchQuota && request.RemainingQuota != nil {
		return BatchResponse{}, ErrInvalidQuery
	}
	response := BatchResponse{Results: make([]BatchItemResult, 0, len(request.AccountIDs))}
	for _, id := range request.AccountIDs {
		item := BatchItemResult{AccountID: id, Status: BatchSucceeded}
		var err error
		switch request.Action {
		case BatchEnable:
			item.AccountStatus = AccountStatusActive
			if service.mutations.Enable == nil {
				err = errors.New("unavailable")
			} else {
				err = service.mutations.Enable(ctx, token, id, request.Reason)
			}
		case BatchDisable:
			item.AccountStatus = AccountStatusDisabled
			if service.mutations.Disable == nil {
				err = errors.New("unavailable")
			} else {
				err = service.mutations.Disable(ctx, token, id, request.Reason)
			}
		case BatchQuota:
			if service.mutations.SetQuota == nil {
				err = errors.New("unavailable")
			} else {
				err = service.mutations.SetQuota(ctx, token, id, uint64(*request.RemainingQuota), request.Reason)
			}
		}
		if err != nil {
			item.Status = BatchFailed
			item.AccountStatus = ""
			item.Error = "operation_failed"
		}
		response.Results = append(response.Results, item)
	}
	return response, nil
}

func validReason(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (service *Service) UpdateRoom(ctx context.Context, id int64, roomID string) (AccountDetail, error) {
	if service == nil || service.db == nil || id <= 0 || !validRoomID(roomID) {
		return AccountDetail{}, ErrInvalidQuery
	}
	if service.mutations.Room == nil {
		return AccountDetail{}, errors.New("unavailable")
	}
	result, err := service.mutations.Room.SetRoom(ctx, id, roomID)
	if err != nil {
		return AccountDetail{}, err
	}
	if result.OldCanonical != "" && !validRoomID(result.OldCanonical) || !validRoomID(result.NewCanonical) {
		return AccountDetail{}, errors.New("unavailable")
	}
	now := time.Now().UTC()
	payload, _ := json.Marshal(map[string]string{"oldRoomId": result.OldCanonical, "newRoomId": result.NewCanonical})
	if _, err := service.db.ExecContext(ctx, `INSERT INTO audit_events (event_type,actor_admin_identity_id,target_account_id,event_data,created_at) VALUES ('admin_room_updated',1,?,?,?)`, id, payload, now); err != nil {
		return AccountDetail{}, err
	}
	return service.Account(ctx, id)
}
func validRoomID(value string) bool {
	if len(value) < 1 || len(value) > 20 || value[0] == '0' {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func encodeCursor(cursor Cursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(value string) (Cursor, error) {
	var cursor Cursor
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID <= 0 || cursor.CreatedAt.IsZero() {
		return Cursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

func normalizeAccountQuery(query AccountQuery) (AccountQuery, error) {
	query.Query = strings.TrimSpace(query.Query)
	if query.Status != "" && query.Status != AccountStatusActive && query.Status != AccountStatusDisabled {
		return AccountQuery{}, ErrInvalidQuery
	}
	if query.Attention != "" && query.Attention != AttentionMissingRoom && query.Attention != AttentionMissingOBS {
		return AccountQuery{}, ErrInvalidQuery
	}
	if query.Limit < 0 {
		return AccountQuery{}, ErrInvalidQuery
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit > 100 {
		query.Limit = 100
	}
	if len(query.Query) > 64 {
		return AccountQuery{}, ErrInvalidQuery
	}
	return query, nil
}

func (service *Service) Overview(ctx context.Context) (Overview, error) {
	var result Overview
	err := service.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(disabled_at IS NULL),0), COALESCE(SUM(disabled_at IS NOT NULL),0), COALESCE(SUM(room.account_id IS NULL),0), COALESCE(SUM(obs.active_account_id IS NULL),0) FROM streamer_accounts a LEFT JOIN account_runtime_rooms room ON room.account_id=a.id LEFT JOIN obs_credentials obs ON obs.active_account_id=a.id`).Scan(&result.TotalAccounts, &result.ActiveAccounts, &result.DisabledAccounts, &result.MissingRooms, &result.MissingOBS)
	if err != nil {
		return Overview{}, err
	}
	rows, err := service.db.QueryContext(ctx, `SELECT a.id, room.account_id IS NULL, obs.active_account_id IS NULL FROM streamer_accounts a LEFT JOIN account_runtime_rooms room ON room.account_id=a.id LEFT JOIN obs_credentials obs ON obs.active_account_id=a.id WHERE room.account_id IS NULL OR obs.active_account_id IS NULL ORDER BY (room.account_id IS NULL) DESC, (obs.active_account_id IS NULL) DESC, a.id LIMIT 10`)
	if err != nil {
		return Overview{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var missingRoom, missingOBS bool
		if err := rows.Scan(&id, &missingRoom, &missingOBS); err != nil {
			return Overview{}, err
		}
		if missingRoom {
			result.Attention = append(result.Attention, AttentionItem{AttentionMissingRoom, id, "尚未设置直播间", 100})
		} else if missingOBS {
			result.Attention = append(result.Attention, AttentionItem{AttentionMissingOBS, id, "尚未创建 OBS 地址", 80})
		}
	}
	result.RecentEvents, err = service.events(ctx, 0, 10)
	return result, err
}

func (service *Service) Accounts(ctx context.Context, raw AccountQuery) (Page[AccountSummary], error) {
	query, err := normalizeAccountQuery(raw)
	if err != nil {
		return Page[AccountSummary]{}, err
	}
	args := []any{}
	where := []string{"1=1"}
	if query.Query != "" {
		id, parseErr := strconv.ParseInt(query.Query, 10, 64)
		if parseErr != nil || id <= 0 {
			return Page[AccountSummary]{}, ErrInvalidQuery
		}
		where = append(where, "(a.id=? OR room.room_id=?)")
		args = append(args, id, query.Query)
	}
	if query.Status == AccountStatusActive {
		where = append(where, "a.disabled_at IS NULL")
	} else if query.Status == AccountStatusDisabled {
		where = append(where, "a.disabled_at IS NOT NULL")
	}
	if query.Attention == AttentionMissingRoom {
		where = append(where, "room.account_id IS NULL")
	} else if query.Attention == AttentionMissingOBS {
		where = append(where, "obs.active_account_id IS NULL")
	}
	if query.Cursor != "" {
		cursor, cursorErr := decodeCursor(query.Cursor)
		if cursorErr != nil {
			return Page[AccountSummary]{}, cursorErr
		}
		where = append(where, "(a.created_at < ? OR (a.created_at = ? AND a.id < ?))")
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	args = append(args, query.Limit+1)
	sqlQuery := `SELECT a.id, a.disabled_at IS NULL, COALESCE(room.room_id,''), COALESCE(quota.remaining_quota,0), obs.active_account_id IS NOT NULL, a.created_at, a.updated_at FROM streamer_accounts a LEFT JOIN account_runtime_rooms room ON room.account_id=a.id LEFT JOIN invitation_quotas quota ON quota.account_id=a.id LEFT JOIN obs_credentials obs ON obs.active_account_id=a.id WHERE ` + strings.Join(where, " AND ") + ` ORDER BY a.created_at DESC,a.id DESC LIMIT ?`
	rows, err := service.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return Page[AccountSummary]{}, err
	}
	defer rows.Close()
	items := make([]AccountSummary, 0, query.Limit+1)
	for rows.Next() {
		var item AccountSummary
		var active bool
		if err := rows.Scan(&item.ID, &active, &item.RoomID, &item.InvitationQuota, &item.HasOBS, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return Page[AccountSummary]{}, err
		}
		if active {
			item.Status = AccountStatusActive
		} else {
			item.Status = AccountStatusDisabled
		}
		items = append(items, item)
	}
	page := Page[AccountSummary]{Items: items}
	if len(items) > query.Limit {
		last := items[query.Limit-1]
		page.Items = items[:query.Limit]
		page.NextCursor = encodeCursor(Cursor{last.CreatedAt, last.ID})
	}
	return page, rows.Err()
}

func (service *Service) Account(ctx context.Context, id int64) (AccountDetail, error) {
	if id <= 0 {
		return AccountDetail{}, ErrInvalidQuery
	}
	var item AccountDetail
	var active bool
	var publicID string
	err := service.db.QueryRowContext(ctx, `SELECT a.id,a.disabled_at IS NULL,COALESCE(room.room_id,''),COALESCE(quota.remaining_quota,0),obs.active_account_id IS NOT NULL,COALESCE(obs.public_id,''),a.created_at,a.updated_at FROM streamer_accounts a LEFT JOIN account_runtime_rooms room ON room.account_id=a.id LEFT JOIN invitation_quotas quota ON quota.account_id=a.id LEFT JOIN obs_credentials obs ON obs.active_account_id=a.id WHERE a.id=?`, id).Scan(&item.ID, &active, &item.RoomID, &item.InvitationQuota, &item.HasOBS, &publicID, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountDetail{}, ErrNotFound
	}
	if err != nil {
		return AccountDetail{}, err
	}
	if active {
		item.Status = AccountStatusActive
	} else {
		item.Status = AccountStatusDisabled
	}
	if publicID != "" {
		item.OBSURL = service.publicOrigin + "/obs/" + publicID
	}
	item.RecentEvents, err = service.events(ctx, id, 20)
	return item, err
}

var eventCopy = map[string]string{"streamer_account_disabled": "账号已停用", "streamer_account_enabled": "账号已启用", "invitation_quota_adjusted": "邀请码额度已调整", "obs_credential_reset": "OBS 地址已更新", "admin_room_updated": "直播间已更新"}

func (service *Service) events(ctx context.Context, accountID int64, limit int) ([]Event, error) {
	q := `SELECT event_type,COALESCE(target_account_id,0),created_at FROM audit_events`
	args := []any{}
	if accountID > 0 {
		q += ` WHERE target_account_id=?`
		args = append(args, accountID)
	}
	q += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := service.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.Type, &event.AccountID, &event.CreatedAt); err != nil {
			return nil, err
		}
		text, ok := eventCopy[event.Type]
		if !ok {
			continue
		}
		event.Text = text
		events = append(events, event)
	}
	return events, rows.Err()
}

func parseAccountID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
		return 0, fmt.Errorf("%w: account id", ErrInvalidQuery)
	}
	return id, nil
}
