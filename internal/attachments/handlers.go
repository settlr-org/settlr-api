package attachments

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
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
	var groupID uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `SELECT group_id FROM expenses WHERE id=$1 AND deleted_at IS NULL`, expenseID).Scan(&groupID)
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
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rows, err := h.Pool.Query(r.Context(), `SELECT id, user_id, file_url, file_name, mime_type, size_bytes, created_at FROM expense_attachments WHERE expense_id=$1 ORDER BY created_at DESC`, expenseID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, uid2 uuid.UUID
		var url, name, mime string
		var size int
		var createdAt any
		_ = rows.Scan(&id, &uid2, &url, &name, &mime, &size, &createdAt)
		out = append(out, map[string]any{"id": id, "user_id": uid2, "file_url": url, "file_name": name, "mime_type": mime, "size_bytes": size, "created_at": createdAt})
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
	var groupID uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `SELECT group_id FROM expenses WHERE id=$1 AND deleted_at IS NULL`, expenseID).Scan(&groupID)
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
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	if !ok {
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
	_, err = h.Pool.Exec(r.Context(), `INSERT INTO expense_attachments (id, expense_id, user_id, file_url, file_name, mime_type, size_bytes) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, expenseID, userID, url, header.Filename, mime, int(size))
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
	var owner uuid.UUID
	var url string
	err = h.Pool.QueryRow(r.Context(), `SELECT user_id, file_url FROM expense_attachments WHERE id=$1`, id).Scan(&owner, &url)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if owner != userID {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `DELETE FROM expense_attachments WHERE id=$1`, id)
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
	var groupID uuid.UUID
	var fileURL, mime string
	err = h.Pool.QueryRow(r.Context(), `
		SELECT e.group_id, a.file_url, a.mime_type
		FROM expense_attachments a JOIN expenses e ON e.id=a.expense_id
		WHERE a.id=$1 AND e.deleted_at IS NULL`, attachmentID).Scan(&groupID, &fileURL, &mime)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, err := uuid.Parse(uid)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	var member bool
	if err := h.Pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM group_members WHERE group_id=$1 AND user_id=$2)`, groupID, userID).Scan(&member); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !member || fileURL != "/uploads/"+name {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", mime)
	fpath := filepath.Join("./uploads", name)
	http.ServeFile(w, r, fpath)
}
