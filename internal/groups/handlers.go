package groups

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/mailer"
)

// Handler handles group-related HTTP requests. All database operations use the
// sqlc-generated query layer; Pool is retained only to begin transactions.
type Handler struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
	Mailer  *mailer.Mailer
}

// queries returns the generated, type-safe query layer for every production
// handler invocation. Keeping this fallback makes package-level handler tests
// that construct a Handler with only Pool continue to work while ensuring the
// HTTP path never needs to hand-write SQL.
func (h *Handler) queries() *db.Queries {
	if h.Queries != nil {
		return h.Queries
	}
	return db.New(h.Pool)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/groups", authMw(http.HandlerFunc(h.ListGroups)))
	mux.Handle("POST /api/v1/groups", authMw(http.HandlerFunc(h.CreateGroup)))
	mux.Handle("GET /api/v1/groups/{id}", authMw(http.HandlerFunc(h.GetGroup)))
	mux.Handle("PATCH /api/v1/groups/{id}", authMw(http.HandlerFunc(h.UpdateGroup)))
	mux.Handle("DELETE /api/v1/groups/{id}", authMw(http.HandlerFunc(h.DeleteGroup)))
	mux.Handle("POST /api/v1/groups/{id}/archive", authMw(http.HandlerFunc(h.ArchiveGroup)))
	mux.Handle("GET /api/v1/groups/{id}/members", authMw(http.HandlerFunc(h.ListMembers)))
	mux.Handle("POST /api/v1/groups/{id}/members", authMw(http.HandlerFunc(h.AddMember)))
	mux.Handle("DELETE /api/v1/groups/{id}/members/{userId}", authMw(http.HandlerFunc(h.RemoveMember)))
	mux.Handle("PATCH /api/v1/groups/{id}/members/{userId}", authMw(http.HandlerFunc(h.UpdateMemberRole)))
	mux.Handle("POST /api/v1/groups/{id}/leave", authMw(http.HandlerFunc(h.LeaveGroup)))
	mux.Handle("GET /api/v1/groups/{id}/activity", authMw(http.HandlerFunc(h.Activity)))
	mux.Handle("POST /api/v1/groups/{id}/invites", authMw(http.HandlerFunc(h.CreateInvite)))
	mux.Handle("GET /api/v1/groups/{id}/invites", authMw(http.HandlerFunc(h.ListInvites)))
	mux.Handle("GET /api/v1/invites", authMw(http.HandlerFunc(h.MyInvites)))
	mux.Handle("POST /api/v1/invites/{token}/accept", authMw(http.HandlerFunc(h.AcceptInvite)))
}

func (h *Handler) mustBeMember(r *http.Request, groupID uuid.UUID) (userID uuid.UUID, role string, ok bool) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ = uuid.Parse(uid)
	// Prefer sqlc-generated IsMember when Queries is wired (type-safe, tested via sqlc vet).
	// This replaces the raw Pool.QueryRow call used in 30+ places across the codebase.
	// See internal/db/queries/groups.sql: IsMember / GetMemberRole.
	if h.queries() != nil {
		var err error
		role, err = h.queries().IsMember(r.Context(), db.IsMemberParams{GroupID: groupID, UserID: userID})
		if err != nil {
			return uuid.Nil, "", false
		}
		return userID, role, true
	}
	return uuid.Nil, "", false
}

func parseGroupID(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	return id, err == nil
}

func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var out []map[string]any
	qdb := h.queries()
	if q != "" {
		rows, err := qdb.ListGroupsFiltered(r.Context(), db.ListGroupsFilteredParams{UserID: userID, Column2: pgtype.Text{String: q, Valid: true}})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		for _, row := range rows {
			out = append(out, map[string]any{"id": row.ID, "name": row.Name, "description": row.Description, "avatar_url": row.AvatarUrl, "currency": row.Currency, "group_type": row.GroupType, "simplify_debts": row.SimplifyDebts, "created_by": row.CreatedBy, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt, "archived_at": row.ArchivedAt, "information": row.Information})
		}
	} else {
		rows, err := qdb.ListGroups(r.Context(), userID)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		for _, row := range rows {
			out = append(out, map[string]any{"id": row.ID, "name": row.Name, "description": row.Description, "avatar_url": row.AvatarUrl, "currency": row.Currency, "group_type": row.GroupType, "simplify_debts": row.SimplifyDebts, "created_by": row.CreatedBy, "created_at": row.CreatedAt, "updated_at": row.UpdatedAt, "archived_at": row.ArchivedAt, "information": row.Information})
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

type createGroupReq struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Currency    *string `json:"currency"`
	AvatarURL   *string `json:"avatar_url"`
	Information *string `json:"information"`
}

func (h *Handler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var req createGroupReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "name required (max 100)"}})
		return
	}
	currency := "NPR"
	if req.Currency != nil {
		currency = strings.ToUpper(strings.TrimSpace(*req.Currency))
		if !auth.SupportedCurrencies[currency] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
	}
	desc := ""
	if req.Description != nil {
		desc = *req.Description
	}
	avatar := ""
	if req.AvatarURL != nil {
		avatar = *req.AvatarURL
	}
	information := ""
	if req.Information != nil {
		information = *req.Information
	}
	groupID := uuid.New()
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.queries().WithTx(tx)
	if err = qtx.CreateGroup(r.Context(), db.CreateGroupParams{
		ID: groupID, Name: req.Name, Description: desc, Column4: avatar, Currency: currency,
		CreatedBy: userID, Information: pgtype.Text{String: information, Valid: information != ""},
	}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = qtx.CreateGroupMember(r.Context(), db.CreateGroupMemberParams{GroupID: groupID, UserID: userID, Role: "OWNER"}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = qtx.InsertActivityEvent(r.Context(), db.InsertActivityEventParams{GroupID: groupID, ActorID: userID, Type: "GROUP_CREATED", EntityType: "group", EntityID: groupID, Payload: json.RawMessage(`{"name":"` + req.Name + `"}`)}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": groupID, "name": req.Name, "description": desc, "currency": currency, "avatar_url": avatar, "information": information})
}

func (h *Handler) GetGroup(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if _, _, ok := h.mustBeMember(r, groupID); !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	group, err := h.queries().GetGroup(r.Context(), groupID)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"id": groupID, "name": group.Name, "description": group.Description, "avatar_url": group.AvatarUrl, "currency": group.Currency, "group_type": group.GroupType, "simplify_debts": group.SimplifyDebts, "created_by": group.CreatedBy, "created_at": group.CreatedAt, "updated_at": group.UpdatedAt, "archived_at": group.ArchivedAt, "information": group.Information})
}

type updateGroupReq struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	AvatarURL     *string `json:"avatar_url"`
	Currency      *string `json:"currency"`
	GroupType     *string `json:"group_type"`
	SimplifyDebts *bool   `json:"simplify_debts"`
	Information   *string `json:"information"`
}

func (h *Handler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	_, role, ok := h.mustBeMember(r, groupID)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	if role != "OWNER" && role != "ADMIN" {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	var req updateGroupReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 100 {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid name"}})
			return
		}
		if err := h.queries().UpdateGroupName(r.Context(), db.UpdateGroupNameParams{ID: groupID, Name: n}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if req.Description != nil {
		if err := h.queries().UpdateGroupDescription(r.Context(), db.UpdateGroupDescriptionParams{ID: groupID, Description: *req.Description}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if req.AvatarURL != nil {
		if err := h.queries().UpdateGroupAvatar(r.Context(), db.UpdateGroupAvatarParams{ID: groupID, AvatarUrl: pgtype.Text{String: *req.AvatarURL, Valid: true}}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if req.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.Currency))
		if !auth.SupportedCurrencies[c] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
		if err := h.queries().UpdateGroupCurrency(r.Context(), db.UpdateGroupCurrencyParams{ID: groupID, Currency: c}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if req.GroupType != nil {
		gt := strings.ToUpper(strings.TrimSpace(*req.GroupType))
		if gt != "HOME" && gt != "TRIP" && gt != "COUPLE" && gt != "OTHER" {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "group_type must be HOME/TRIP/COUPLE/OTHER"}})
			return
		}
		if err := h.queries().UpdateGroupType(r.Context(), db.UpdateGroupTypeParams{ID: groupID, GroupType: gt}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if req.SimplifyDebts != nil {
		if err := h.queries().UpdateGroupSimplifyDebts(r.Context(), db.UpdateGroupSimplifyDebtsParams{ID: groupID, SimplifyDebts: *req.SimplifyDebts}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if req.Information != nil {
		if err := h.queries().UpdateGroupInformation(r.Context(), db.UpdateGroupInformationParams{ID: groupID, Information: pgtype.Text{String: *req.Information, Valid: true}}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	// Also handle share link via invite_token (capability URL) — generate if missing
	if r.URL.Query().Get("generate_share_link") == "1" {
		var token string
		inviteToken, _ := h.queries().GetInviteToken(r.Context(), groupID)
		token = inviteToken.String
		if token == "" {
			raw, _ := auth.GenerateRefreshToken()
			if err := h.queries().SetInviteToken(r.Context(), db.SetInviteTokenParams{ID: groupID, InviteToken: pgtype.Text{String: raw[:12], Valid: true}}); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
		}
	}
	h.GetGroup(w, r)
}

func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	_, role, ok := h.mustBeMember(r, groupID)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	if role != "OWNER" {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.queries().WithTx(tx)
	// Delete dependent records in FK order before removing the ledger.
	for _, remove := range []func(context.Context, uuid.UUID) error{
		qtx.DeleteGroupSettlements,
		qtx.DeleteGroupExpenseAttachments,
		qtx.DeleteGroupExpenseSplits,
		qtx.DeleteGroupExpenses,
		qtx.DeleteGroupRecurring,
		qtx.DeleteGroup,
	} {
		if err := remove(r.Context(), groupID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "group deleted"})
}

func (h *Handler) ArchiveGroup(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	_, role, ok := h.mustBeMember(r, groupID)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	if role != "OWNER" && role != "ADMIN" {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	if err := h.queries().ArchiveGroup(r.Context(), groupID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "group archived"})
}

func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if _, _, ok := h.mustBeMember(r, groupID); !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rows, err := h.queries().ListGroupMembers(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		out = append(out, map[string]any{"id": row.ID, "name": row.Name, "avatar_url": row.AvatarUrl, "role": row.Role, "joined_at": row.JoinedAt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

type addMemberReq struct {
	UserID *string `json:"user_id"`
	Role   *string `json:"role"`
}

func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	_, role, ok := h.mustBeMember(r, groupID)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	if role != "OWNER" && role != "ADMIN" {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	var req addMemberReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	var targetID uuid.UUID
	if req.UserID == nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "user_id required"}})
		return
	}
	id, err := uuid.Parse(*req.UserID)
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user_id"}})
		return
	}
	targetID = id
	viewerID, _, _ := h.mustBeMember(r, groupID)
	isFriend, err := h.queries().CheckFriendship(r.Context(), db.CheckFriendshipParams{Column1: pgtype.UUID{Bytes: viewerID, Valid: true}, Column2: pgtype.UUID{Bytes: targetID, Valid: true}})
	if err != nil || !isFriend {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "only friends can be added to a group"}})
		return
	}
	newRole := "MEMBER"
	if req.Role != nil {
		if *req.Role != "ADMIN" && *req.Role != "MEMBER" {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "role must be ADMIN or MEMBER"}})
			return
		}
		newRole = *req.Role
	}
	alreadyMember, err := h.queries().CheckAlreadyMember(r.Context(), db.CheckAlreadyMemberParams{GroupID: groupID, UserID: targetID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if alreadyMember {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "user is already a member"}})
		return
	}
	if err := h.queries().AddGroupMember(r.Context(), db.AddGroupMemberParams{GroupID: groupID, UserID: targetID, Role: newRole}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	actorID, _ := uuid.Parse(uid)
	_ = h.queries().InsertActivityEvent(r.Context(), db.InsertActivityEventParams{GroupID: groupID, ActorID: actorID, Type: "MEMBER_ADDED", EntityType: "user", EntityID: targetID, Payload: json.RawMessage(`{}`)})
	httpx.WriteJSON(w, 201, map[string]any{"message": "member added", "user_id": targetID, "role": newRole})
}

func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	_, role, ok := h.mustBeMember(r, groupID)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	if role != "OWNER" && role != "ADMIN" {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	targetID, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	_ = h.queries().RemoveGroupMember(r.Context(), db.RemoveGroupMemberParams{GroupID: groupID, UserID: targetID})
	uid, _ := httpx.GetUserID(r.Context())
	actorID, _ := uuid.Parse(uid)
	_ = h.queries().InsertActivityEvent(r.Context(), db.InsertActivityEventParams{GroupID: groupID, ActorID: actorID, Type: "MEMBER_REMOVED", EntityType: "user", EntityID: targetID, Payload: json.RawMessage(`{}`)})
	httpx.WriteJSON(w, 200, map[string]any{"message": "member removed"})
}

func (h *Handler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	_, role, ok := h.mustBeMember(r, groupID)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	if role != "OWNER" {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	targetID, err := uuid.Parse(r.PathValue("userId"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if req.Role != "ADMIN" && req.Role != "MEMBER" && req.Role != "OWNER" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid role"}})
		return
	}
	member, err := h.queries().GetGroupMember(r.Context(), db.GetGroupMemberParams{GroupID: groupID, UserID: targetID})
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = h.queries().UpdateMemberRole(r.Context(), db.UpdateMemberRoleParams{GroupID: groupID, UserID: targetID, Role: req.Role}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"member": map[string]any{"id": targetID, "name": member.Name, "avatar_url": member.AvatarUrl, "role": req.Role, "joined_at": member.JoinedAt}})
}

func (h *Handler) LeaveGroup(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	if _, _, ok := h.mustBeMember(r, groupID); !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	_ = h.queries().LeaveGroup(r.Context(), db.LeaveGroupParams{GroupID: groupID, UserID: userID})
	_ = h.queries().InsertActivityEvent(r.Context(), db.InsertActivityEventParams{GroupID: groupID, ActorID: userID, Type: "MEMBER_LEFT", EntityType: "user", EntityID: userID, Payload: json.RawMessage(`{}`)})
	httpx.WriteJSON(w, 200, map[string]any{"message": "left group"})
}

func (h *Handler) Activity(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if _, _, ok := h.mustBeMember(r, groupID); !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	q := r.URL.Query()
	limit := 50
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	cursor := q.Get("cursor")
	var out []map[string]any
	var lastID *uuid.UUID
	if cid, err := uuid.Parse(cursor); cursor != "" && err == nil {
		rows, err := h.queries().ListGroupActivityBefore(r.Context(), db.ListGroupActivityBeforeParams{GroupID: groupID, ID: cid, Limit: int32(limit + 1)})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		for _, row := range rows {
			id := row.ID
			out = append(out, map[string]any{"id": row.ID, "actor_id": row.ActorID, "type": row.Type, "entity_type": row.EntityType, "entity_id": row.EntityID, "payload": row.Payload, "created_at": row.CreatedAt})
			lastID = &id
		}
	} else {
		rows, err := h.queries().ListGroupActivity(r.Context(), db.ListGroupActivityParams{GroupID: groupID, Limit: int32(limit + 1)})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		for _, row := range rows {
			id := row.ID
			out = append(out, map[string]any{"id": row.ID, "actor_id": row.ActorID, "type": row.Type, "entity_type": row.EntityType, "entity_id": row.EntityID, "payload": row.Payload, "created_at": row.CreatedAt})
			lastID = &id
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	var nextCursor *string
	if len(out) > limit {
		out = out[:limit]
		if lastID != nil {
			s := lastID.String()
			nextCursor = &s
		}
	}
	resp := map[string]any{"data": out}
	if nextCursor != nil {
		resp["next_cursor"] = *nextCursor
	}
	httpx.WriteJSON(w, 200, resp)
}

func (h *Handler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if _, _, ok := h.mustBeMember(r, groupID); !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	// Any member may invite (Splitwise behavior)
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil || req.UserID == "" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "user_id required"}})
		return
	}
	friendID, err := uuid.Parse(req.UserID)
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user_id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	invitedBy, _ := uuid.Parse(uid)
	email, err := h.queries().GetUserEmail(r.Context(), friendID)
	if err != nil {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "only friends can be invited to a group"}})
		return
	}
	isFriend, err := h.queries().CheckFriendship(r.Context(), db.CheckFriendshipParams{Column1: pgtype.UUID{Bytes: invitedBy, Valid: true}, Column2: pgtype.UUID{Bytes: friendID, Valid: true}})
	if err != nil || !isFriend {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "only friends can be invited to a group"}})
		return
	}
	// Already member?
	exists, err := h.queries().CheckGroupMemberEmail(r.Context(), db.CheckGroupMemberEmailParams{GroupID: groupID, Lower: email})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if exists {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "user already a member"}})
		return
	}
	raw, hash := auth.GenerateRefreshToken()
	inviteID := uuid.New()
	if err = h.queries().CreateGroupInvite(r.Context(), db.CreateGroupInviteParams{ID: inviteID, GroupID: groupID, Email: email, TokenHash: hash, InvitedBy: invitedBy}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// If email belongs to existing user, create notification
	if targetID, err := h.queries().GetGroupInviteeIDByEmail(r.Context(), email); err == nil {
		_ = h.queries().CreateGroupInviteNotification(r.Context(), db.CreateGroupInviteNotificationParams{UserID: targetID, Title: "Group invitation", Body: "You've been invited to join a group", Data: json.RawMessage(`{"group_id":"` + groupID.String() + `","token":"` + raw + `"}`)})
	}
	// Email the invite link to the selected accepted friend.
	if h.Mailer != nil {
		var groupName, inviterName string
		groupName, _ = h.queries().GetGroupName(r.Context(), groupID)
		inviterName, _ = h.queries().GetUserNameByID(r.Context(), invitedBy)
		subject, html := h.Mailer.GroupInviteEmail(email, groupName, inviterName, raw)
		h.Mailer.SendAsync(email, subject, html)
	}
	httpx.WriteJSON(w, 201, map[string]any{"invite_id": inviteID, "email": email, "token": raw, "expires_at": "7d"})
}

func (h *Handler) ListInvites(w http.ResponseWriter, r *http.Request) {
	groupID, ok := parseGroupID(r)
	if !ok {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if _, _, ok := h.mustBeMember(r, groupID); !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rows, err := h.queries().ListGroupInvites(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		out = append(out, map[string]any{"id": row.ID, "email": row.Email, "status": row.Status, "invited_by": row.InvitedBy, "created_at": row.CreatedAt, "expires_at": row.ExpiresAt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) MyInvites(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	email, _ := h.queries().GetUserEmail(r.Context(), userID)
	rows, err := h.queries().ListMyInvites(r.Context(), email)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		out = append(out, map[string]any{"id": row.ID, "group_id": row.GroupID, "group_name": row.GroupName, "email": row.Email, "created_at": row.CreatedAt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "token required"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.queries().WithTx(tx)
	hash := auth.HashToken(token)
	invite, err := qtx.GetInviteByHashForUpdate(r.Context(), hash)
	if err == pgx.ErrNoRows {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "invalid or expired invite"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if invite.Status != "PENDING" {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "invite has already been accepted"}})
		return
	}
	expiresValid, err := qtx.IsGroupInviteCurrent(r.Context(), invite.ID)
	if err != nil || !expiresValid {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "invalid or expired invite"}})
		return
	}
	userEmail, _ := qtx.GetUserEmail(r.Context(), userID)
	if userEmail != strings.ToLower(invite.Email) {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "invite email does not match your account"}})
		return
	}
	// An invite is issued only to an accepted friend.  Ensure that relationship is
	// present when it is redeemed too, so an invited account remains connected to
	// its inviter even if an earlier relationship was removed before acceptance.
	if err := qtx.EnsureFriendship(r.Context(), db.EnsureFriendshipParams{ID: uuid.New(), Column2: pgtype.UUID{Bytes: userID, Valid: true}, ActionBy: pgtype.UUID{Bytes: invite.InvitedBy, Valid: true}}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	alreadyMember, err := qtx.CheckAlreadyMember(r.Context(), db.CheckAlreadyMemberParams{GroupID: invite.GroupID, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if alreadyMember {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "you are already a member of this group"}})
		return
	}
	if err := qtx.AcceptGroupInviteMember(r.Context(), db.AcceptGroupInviteMemberParams{GroupID: invite.GroupID, UserID: userID}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := qtx.MarkInviteAccepted(r.Context(), invite.ID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := qtx.InsertActivityEvent(r.Context(), db.InsertActivityEventParams{GroupID: invite.GroupID, ActorID: userID, Type: "MEMBER_ADDED", EntityType: "user", EntityID: userID, Payload: json.RawMessage(`{}`)}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "invite accepted", "group_id": invite.GroupID})
}
