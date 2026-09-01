package attachments

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

type Handler struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
}

func (h *Handler) ensureQueries() *db.Queries {
	if h.Queries != nil {
		return h.Queries
	}
	if h.Pool != nil {
		return db.New(h.Pool)
	}
	return nil
}

func (h *Handler) isMember(ctx context.Context, groupID, userID uuid.UUID) bool {
	q := h.ensureQueries()
	if ok, err := q.IsMember(ctx, db.IsMemberParams{GroupID: groupID, UserID: userID}); err == nil && ok != "" {
		return true
	}
	if ok, err := q.CheckAttachmentGroupMember(ctx, db.CheckAttachmentGroupMemberParams{GroupID: groupID, UserID: userID}); err == nil {
		return ok
	}
	return false
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/expenses/{id}/attachments", authMw(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/v1/expenses/{id}/attachments", authMw(http.HandlerFunc(h.Upload)))
	mux.Handle("DELETE /api/v1/attachments/{id}", authMw(http.HandlerFunc(h.Delete)))
	mux.Handle("GET /uploads/{id}", authMw(http.HandlerFunc(h.Serve)))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	q := h.ensureQueries()
	groupID, err := q.GetExpenseGroupIDForAttachments(r.Context(), expenseID)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	if !h.isMember(r.Context(), groupID, userID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rows, qerr := q.ListAttachments(r.Context(), expenseID)
	if qerr != nil {
		httpx.WriteError(w, r, qerr)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		out = append(out, map[string]any{"id": row.ID, "user_id": row.UserID, "file_url": row.FileUrl, "file_name": row.FileName, "mime_type": row.MimeType, "size_bytes": row.SizeBytes, "created_at": row.CreatedAt.Time})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	q := h.ensureQueries()
	groupID, err := q.GetExpenseGroupIDForAttachments(r.Context(), expenseID)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	if !h.isMember(r.Context(), groupID, userID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20) // 5MB
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httpx.WriteJSON(w, 413, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "file too large (max 5MB)"}})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "file field required"}})
		return
	}
	defer file.Close()
	if header.Size > 5<<20 {
		httpx.WriteJSON(w, 413, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "file too large"}})
		return
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".pdf" && ext != ".webp" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported file type"}})
		return
	}
	id := uuid.New()
	dir := "./uploads"
	if err := os.MkdirAll(dir, 0755); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	fname := fmt.Sprintf("%s%s", id.String(), ext)
	fpath := filepath.Join(dir, fname)
	out, err := os.Create(fpath)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	size, copyErr := io.Copy(out, file)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(fpath)
		if copyErr != nil {
			httpx.WriteError(w, r, copyErr)
		} else {
			httpx.WriteError(w, r, closeErr)
		}
		return
	}
	url := "/uploads/" + fname
	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	err = q.CreateAttachment(r.Context(), db.CreateAttachmentParams{
		ID: id, ExpenseID: expenseID, UserID: userID, FileUrl: url, FileName: header.Filename, MimeType: mime, SizeBytes: int32(size),
	})
	if err != nil {
		_ = os.Remove(fpath)
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "file_url": url, "file_name": header.Filename, "mime_type": mime, "size_bytes": size})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid attachment id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	row, err := q.GetAttachmentOwner(r.Context(), id)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	owner := row.UserID
	url := row.FileUrl
	if owner != userID {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	_ = q.DeleteAttachment(r.Context(), id)
	// remove file
	if strings.HasPrefix(url, "/uploads/") {
		_ = os.Remove(filepath.Join("./uploads", strings.TrimPrefix(url, "/uploads/")))
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}

func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("id"))
	attachmentID, err := uuid.Parse(strings.TrimSuffix(name, filepath.Ext(name)))
	if err != nil || name != r.PathValue("id") {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	q := h.ensureQueries()
	row, err := q.GetAttachmentForServe(r.Context(), attachmentID)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	groupID := row.GroupID
	fileURL := row.FileUrl
	mime := row.MimeType
	uid, _ := httpx.GetUserID(r.Context())
	userID, err := uuid.Parse(uid)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	member, _ := q.CheckAttachmentGroupMember(r.Context(), db.CheckAttachmentGroupMemberParams{GroupID: groupID, UserID: userID})
	if !member || fileURL != "/uploads/"+name {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", mime)
	fpath := filepath.Join("./uploads", name)
	http.ServeFile(w, r, fpath)
}
