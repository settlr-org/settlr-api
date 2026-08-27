package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *AppError) Error() string { return e.Message }

var (
	ErrUnauthorized        = &AppError{Code: "UNAUTHORIZED", Message: "authentication required", Status: http.StatusUnauthorized}
	ErrForbidden           = &AppError{Code: "FORBIDDEN", Message: "insufficient permission", Status: http.StatusForbidden}
	ErrNotFound            = &AppError{Code: "NOT_FOUND", Message: "resource not found", Status: http.StatusNotFound}
	ErrValidation          = &AppError{Code: "VALIDATION_ERROR", Message: "validation failed", Status: http.StatusUnprocessableEntity}
	ErrConflict            = &AppError{Code: "CONFLICT", Message: "conflict", Status: http.StatusConflict}
	ErrTooManyRequests     = &AppError{Code: "RATE_LIMITED", Message: "too many requests", Status: http.StatusTooManyRequests}
	ErrInvalidSplit        = &AppError{Code: "INVALID_SPLIT", Message: "expense splits must sum to the total amount", Status: http.StatusUnprocessableEntity}
	ErrGroupMemberRequired = &AppError{Code: "GROUP_MEMBER_REQUIRED", Message: "must be a group member", Status: http.StatusForbidden}
)

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	if ae, ok := err.(*AppError); ok {
		WriteJSON(w, ae.Status, map[string]any{"error": map[string]string{"code": ae.Code, "message": ae.Message}})
		return
	}
	slog.Error("internal error", "error", err.Error(), "path", r.URL.Path)
	WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "INTERNAL", "message": "internal server error"}})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func DecodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
