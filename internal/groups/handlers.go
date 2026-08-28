package groups

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/auth"
	"github.com/nabinkhanal00/settlr-api/internal/httpx"
	"github.com/nabinkhanal00/settlr-api/internal/mailer"
)

type Handler struct {
	Pool   *pgxpool.Pool
	Mailer *mailer.Mailer
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
	err := h.Pool.QueryRow(r.Context(), `SELECT role FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&role)
	if err != nil {
		return uuid.Nil, "", false
	}
	return userID, role, true
}

func parseGroupID(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	return id, err == nil
}

func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var rows pgx.Rows
	var err error
	if q != "" {
		rows, err = h.Pool.Query(r.Context(),
			`SELECT g.id, g.name, g.description, coalesce(g.avatar_url,''), g.currency, coalesce(g.group_type,'OTHER'), coalesce(g.simplify_debts,true), g.created_by, g.created_at, g.updated_at, g.archived_at, g.information
			 FROM groups g JOIN group_members gm ON gm.group_id=g.id
			 WHERE gm.user_id=$1 AND g.archived_at IS NULL AND g.group_type <> 'DIRECT' AND g.name ILIKE '%' || $2 || '%'
			 ORDER BY g.updated_at DESC`, userID, q)
	} else {
		rows, err = h.Pool.Query(r.Context(),
			`SELECT g.id, g.name, g.description, coalesce(g.avatar_url,''), g.currency, coalesce(g.group_type,'OTHER'), coalesce(g.simplify_debts,true), g.created_by, g.created_at, g.updated_at, g.archived_at, g.information
			 FROM groups g JOIN group_members gm ON gm.group_id=g.id
			 WHERE gm.user_id=$1 AND g.archived_at IS NULL AND g.group_type <> 'DIRECT'
			 ORDER BY g.updated_at DESC`, userID)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var name, desc, avatar, currency, groupType string
		var simplifyDebts bool
		var createdBy uuid.UUID
		var createdAt, updatedAt any
		var archivedAt *string
		var information *string
		_ = rows.Scan(&id, &name, &desc, &avatar, &currency, &groupType, &simplifyDebts, &createdBy, &createdAt, &updatedAt, &archivedAt, &information)
		out = append(out, map[string]any{"id": id, "name": name, "description": desc, "avatar_url": avatar, "currency": currency, "group_type": groupType, "simplify_debts": simplifyDebts, "created_by": createdBy, "created_at": createdAt, "updated_at": updatedAt, "archived_at": archivedAt, "information": information})
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
	_, err = tx.Exec(r.Context(),
		`INSERT INTO groups (id, name, description, avatar_url, currency, created_by, information) VALUES ($1,$2,$3, NULLIF($4,''), $5, $6, $7)`,
		groupID, req.Name, desc, avatar, currency, userID, information)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO group_members (group_id, user_id, role) VALUES ($1,$2,'OWNER')`, groupID, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, _ = tx.Exec(r.Context(),
		`INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'GROUP_CREATED','group',$1,$3)`,
		groupID, userID, json.RawMessage(`{"name":"`+req.Name+`"}`))
	_ = tx.Commit(r.Context())
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
	var name, desc, avatar, currency, groupType string
	var information *string
	var simplifyDebts bool
	var createdBy uuid.UUID
	var createdAt, updatedAt any
	var archivedAt *string
	err := h.Pool.QueryRow(r.Context(),
		`SELECT name, description, coalesce(avatar_url,''), currency, coalesce(group_type,'OTHER'), coalesce(simplify_debts,true), created_by, created_at, updated_at, archived_at::text, information FROM groups WHERE id=$1`, groupID).
		Scan(&name, &desc, &avatar, &currency, &groupType, &simplifyDebts, &createdBy, &createdAt, &updatedAt, &archivedAt, &information)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"id": groupID, "name": name, "description": desc, "avatar_url": avatar, "currency": currency, "group_type": groupType, "simplify_debts": simplifyDebts, "created_by": createdBy, "created_at": createdAt, "updated_at": updatedAt, "archived_at": archivedAt, "information": information})
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
		_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET name=$1, updated_at=now() WHERE id=$2`, n, groupID)
	}
	if req.Description != nil {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET description=$1, updated_at=now() WHERE id=$2`, *req.Description, groupID)
	}
	if req.AvatarURL != nil {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET avatar_url=$1, updated_at=now() WHERE id=$2`, *req.AvatarURL, groupID)
	}
	if req.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.Currency))
		if !auth.SupportedCurrencies[c] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
		_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET currency=$1, updated_at=now() WHERE id=$2`, c, groupID)
	}
	if req.GroupType != nil {
		gt := strings.ToUpper(strings.TrimSpace(*req.GroupType))
		if gt != "HOME" && gt != "TRIP" && gt != "COUPLE" && gt != "OTHER" {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "group_type must be HOME/TRIP/COUPLE/OTHER"}})
			return
		}
		_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET group_type=$1, updated_at=now() WHERE id=$2`, gt, groupID)
	}
	if req.SimplifyDebts != nil {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET simplify_debts=$1, updated_at=now() WHERE id=$2`, *req.SimplifyDebts, groupID)
	}
	if req.Information != nil {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET information=$1, updated_at=now() WHERE id=$2`, *req.Information, groupID)
	}
	// Also handle share link via invite_token (capability URL) — generate if missing
	if r.URL.Query().Get("generate_share_link") == "1" {
		var token string
		_ = h.Pool.QueryRow(r.Context(), `SELECT invite_token FROM groups WHERE id=$1`, groupID).Scan(&token)
		if token == "" {
			raw, _ := auth.GenerateRefreshToken()
			_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET invite_token=$1 WHERE id=$2`, raw[:12], groupID)
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
	res, err := h.Pool.Exec(r.Context(), `DELETE FROM groups WHERE id=$1`, groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
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
	_, _ = h.Pool.Exec(r.Context(), `UPDATE groups SET archived_at=now(), updated_at=now() WHERE id=$1`, groupID)
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
	rows, err := h.Pool.Query(r.Context(),
		`SELECT u.id, u.name, coalesce(u.avatar_url,''), gm.role, gm.joined_at
		 FROM group_members gm JOIN users u ON u.id=gm.user_id
		 WHERE gm.group_id=$1 ORDER BY gm.joined_at`, groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var name, avatar, role string
		var joinedAt any
		_ = rows.Scan(&id, &name, &avatar, &role, &joinedAt)
		out = append(out, map[string]any{"id": id, "name": name, "avatar_url": avatar, "role": role, "joined_at": joinedAt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

type addMemberReq struct {
	UserID *string `json:"user_id"`
	Email  *string `json:"email"`
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
	if req.UserID != nil {
		id, err := uuid.Parse(*req.UserID)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user_id"}})
			return
		}
		targetID = id
	} else if req.Email != nil {
		err := h.Pool.QueryRow(r.Context(), `SELECT id FROM users WHERE lower(email)=lower($1)`, *req.Email).Scan(&targetID)
		if err == pgx.ErrNoRows {
			httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "user not found"}})
			return
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	} else {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "user_id or email required"}})
		return
	}
	viewerID, _, _ := h.mustBeMember(r, groupID)
	var isFriend bool
	if err := h.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM friendships WHERE LEAST(user_id, friend_id)=LEAST($1::uuid,$2::uuid) AND GREATEST(user_id, friend_id)=GREATEST($1::uuid,$2::uuid) AND status='ACCEPTED')`, viewerID, targetID).Scan(&isFriend); err != nil || !isFriend {
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
	res, err := h.Pool.Exec(r.Context(), `INSERT INTO group_members (group_id, user_id, role) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, groupID, targetID, newRole)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.RowsAffected() == 0 {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "user is already a member"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	actorID, _ := uuid.Parse(uid)
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'MEMBER_ADDED','user',$3,$4)`,
		groupID, actorID, targetID, json.RawMessage(`{}`))
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
	_, _ = h.Pool.Exec(r.Context(), `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, targetID)
	uid, _ := httpx.GetUserID(r.Context())
	actorID, _ := uuid.Parse(uid)
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'MEMBER_REMOVED','user',$3,$4)`,
		groupID, actorID, targetID, json.RawMessage(`{}`))
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
	res, err := h.Pool.Exec(r.Context(), `UPDATE group_members SET role=$1 WHERE group_id=$2 AND user_id=$3`, req.Role, groupID, targetID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var name, avatar string
	var joinedAt any
	err = h.Pool.QueryRow(r.Context(),
		`SELECT u.name, coalesce(u.avatar_url,''), gm.joined_at
		 FROM group_members gm JOIN users u ON u.id=gm.user_id
		 WHERE gm.group_id=$1 AND gm.user_id=$2`, groupID, targetID).Scan(&name, &avatar, &joinedAt)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"member": map[string]any{"id": targetID, "name": name, "avatar_url": avatar, "role": req.Role, "joined_at": joinedAt}})
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
	_, _ = h.Pool.Exec(r.Context(), `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID)
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'MEMBER_LEFT','user',$3,$4)`,
		groupID, userID, userID, json.RawMessage(`{}`))
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
	where := `group_id=$1`
	args := []any{groupID}
	idx := 2
	if cursor != "" {
		if cid, err := uuid.Parse(cursor); err == nil {
			where += ` AND (created_at, id) < (SELECT created_at, id FROM activity_events WHERE id=$` + strconv.Itoa(idx) + `)`
			args = append(args, cid)
			idx++
		}
	}
	query := `SELECT id, actor_id, type, entity_type, entity_id, payload, created_at FROM activity_events WHERE ` + where + ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(idx)
	args = append(args, limit+1)
	rows, err := h.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	var lastID *uuid.UUID
	for rows.Next() {
		var id, actorID, entityID *uuid.UUID
		var typ, entityType string
		var payload json.RawMessage
		var createdAt any
		_ = rows.Scan(&id, &actorID, &typ, &entityType, &entityID, &payload, &createdAt)
		out = append(out, map[string]any{"id": id, "actor_id": actorID, "type": typ, "entity_type": entityType, "entity_id": entityID, "payload": payload, "created_at": createdAt})
		tmp := *id
		lastID = &tmp
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
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil || req.Email == "" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "email required"}})
		return
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if !strings.Contains(email, "@") {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid email"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	invitedBy, _ := uuid.Parse(uid)
	var friendID uuid.UUID
	if err := h.Pool.QueryRow(r.Context(), `SELECT id FROM users WHERE lower(email)=lower($1)`, email).Scan(&friendID); err != nil {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "only friends can be invited to a group"}})
		return
	}
	var isFriend bool
	if err := h.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM friendships WHERE LEAST(user_id, friend_id)=LEAST($1::uuid,$2::uuid) AND GREATEST(user_id, friend_id)=GREATEST($1::uuid,$2::uuid) AND status='ACCEPTED')`, invitedBy, friendID).Scan(&isFriend); err != nil || !isFriend {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "only friends can be invited to a group"}})
		return
	}
	// Already member?
	var exists bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM users u JOIN group_members gm ON gm.user_id=u.id WHERE gm.group_id=$1 AND lower(u.email)=lower($2)`, groupID, email).Scan(&exists)
	if exists {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "user already a member"}})
		return
	}
	raw, hash := auth.GenerateRefreshToken()
	inviteID := uuid.New()
	_, err := h.Pool.Exec(r.Context(),
		`INSERT INTO group_invites (id, group_id, email, token_hash, invited_by) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (group_id, lower(email)) WHERE status='PENDING' DO UPDATE SET token_hash=$4, invited_by=$5, created_at=now(), expires_at=now()+interval '7 days'`,
		inviteID, groupID, email, hash, invitedBy)
	if err != nil {
		// Fallback for case where ON CONFLICT target not matched: try plain insert
		_, _ = h.Pool.Exec(r.Context(), `DELETE FROM group_invites WHERE group_id=$1 AND lower(email)=lower($2) AND status='PENDING'`, groupID, email)
		_, err = h.Pool.Exec(r.Context(), `INSERT INTO group_invites (id, group_id, email, token_hash, invited_by) VALUES ($1,$2,$3,$4,$5)`, inviteID, groupID, email, hash, invitedBy)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	// If email belongs to existing user, create notification
	var targetID uuid.UUID
	if err := h.Pool.QueryRow(r.Context(), `SELECT id FROM users WHERE lower(email)=lower($1)`, email).Scan(&targetID); err == nil {
		_, _ = h.Pool.Exec(r.Context(), `INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1,'GROUP_INVITATION',$2,$3,$4)`,
			targetID, "Group invitation", "You've been invited to join a group", json.RawMessage(`{"group_id":"`+groupID.String()+`","token":"`+raw+`"}`))
	}
	// Email the invite link (works for members and non-registered emails alike)
	if h.Mailer != nil {
		var groupName, inviterName string
		_ = h.Pool.QueryRow(r.Context(), `SELECT name FROM groups WHERE id=$1`, groupID).Scan(&groupName)
		_ = h.Pool.QueryRow(r.Context(), `SELECT name FROM users WHERE id=$1`, invitedBy).Scan(&inviterName)
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
	rows, err := h.Pool.Query(r.Context(), `SELECT id, email, status, invited_by, created_at, expires_at FROM group_invites WHERE group_id=$1 AND status='PENDING' ORDER BY created_at DESC`, groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, invitedBy uuid.UUID
		var email, status string
		var createdAt, expiresAt any
		_ = rows.Scan(&id, &email, &status, &invitedBy, &createdAt, &expiresAt)
		out = append(out, map[string]any{"id": id, "email": email, "status": status, "invited_by": invitedBy, "created_at": createdAt, "expires_at": expiresAt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) MyInvites(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var email string
	_ = h.Pool.QueryRow(r.Context(), `SELECT lower(email) FROM users WHERE id=$1`, userID).Scan(&email)
	rows, err := h.Pool.Query(r.Context(), `SELECT gi.id, gi.group_id, g.name, gi.email, gi.created_at FROM group_invites gi JOIN groups g ON g.id=gi.group_id WHERE lower(gi.email)=lower($1) AND gi.status='PENDING' AND gi.expires_at > now()`, email)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, gid uuid.UUID
		var gname, e string
		var createdAt any
		_ = rows.Scan(&id, &gid, &gname, &e, &createdAt)
		out = append(out, map[string]any{"id": id, "group_id": gid, "group_name": gname, "email": e, "created_at": createdAt})
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
	hash := auth.HashToken(token)
	var inviteID, groupID, invitedBy uuid.UUID
	var email, status string
	err = tx.QueryRow(r.Context(), `SELECT id, group_id, email, invited_by, status FROM group_invites WHERE token_hash=$1 FOR UPDATE`, hash).Scan(&inviteID, &groupID, &email, &invitedBy, &status)
	if err == pgx.ErrNoRows {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "invalid or expired invite"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if status != "PENDING" {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "invite has already been accepted"}})
		return
	}
	var expiresValid bool
	if err := tx.QueryRow(r.Context(), `SELECT expires_at > now() FROM group_invites WHERE id=$1`, inviteID).Scan(&expiresValid); err != nil || !expiresValid {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "invalid or expired invite"}})
		return
	}
	var userEmail string
	_ = tx.QueryRow(r.Context(), `SELECT lower(email) FROM users WHERE id=$1`, userID).Scan(&userEmail)
	if userEmail != strings.ToLower(email) {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "invite email does not match your account"}})
		return
	}
	var areFriends bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM friendships WHERE LEAST(user_id, friend_id)=LEAST($1::uuid,$2::uuid) AND GREATEST(user_id, friend_id)=GREATEST($1::uuid,$2::uuid) AND status='ACCEPTED')`, userID, invitedBy).Scan(&areFriends); err != nil || !areFriends {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "only friends can join this group"}})
		return
	}
	var alreadyMember bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id=$1 AND user_id=$2)`, groupID, userID).Scan(&alreadyMember); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if alreadyMember {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "you are already a member of this group"}})
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO group_members (group_id, user_id, role) VALUES ($1,$2,'MEMBER')`, groupID, userID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE group_invites SET status='ACCEPTED' WHERE id=$1 AND status='PENDING'`, inviteID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'MEMBER_ADDED','user',$3,$4)`, groupID, userID, userID, json.RawMessage(`{}`)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "invite accepted", "group_id": groupID})
}
