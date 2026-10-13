package wallet

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

var reminderTimeRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

var themes = map[string]bool{"light": true, "dark": true, "auto": true, "amoled": true}

func defaultSettings(userID uuid.UUID) db.UserSetting {
	return db.UserSetting{UserID: userID, MonthStartDay: 1, Theme: "auto", ReminderTime: "20:00"}
}

// loadSettings returns the stored settings or the defaults when there is no row.
func (h *Handler) loadSettings(ctx context.Context, userID uuid.UUID) (db.UserSetting, error) {
	s, err := h.q().GetUserSettings(ctx, userID)
	if isNoRows(err) {
		return defaultSettings(userID), nil
	}
	return s, err
}

func settingsJSON(s db.UserSetting) map[string]any {
	return map[string]any{
		"month_start_day":         s.MonthStartDay,
		"hide_balance":            s.HideBalance,
		"hide_income":             s.HideIncome,
		"treat_transfers_as_flow": s.TreatTransfersAsFlow,
		"theme":                   s.Theme,
		"reminder_enabled":        s.ReminderEnabled,
		"reminder_time":           s.ReminderTime,
		"onboarded_at":            tsPtr(s.OnboardedAt),
	}
}

func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.loadSettings(r.Context(), currentUser(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settingsJSON(s))
}

func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	s, err := h.loadSettings(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		MonthStartDay        Opt[int32]  `json:"month_start_day"`
		HideBalance          Opt[bool]   `json:"hide_balance"`
		HideIncome           Opt[bool]   `json:"hide_income"`
		TreatTransfersAsFlow Opt[bool]   `json:"treat_transfers_as_flow"`
		Theme                Opt[string] `json:"theme"`
		ReminderEnabled      Opt[bool]   `json:"reminder_enabled"`
		ReminderTime         Opt[string] `json:"reminder_time"`
		OnboardedAt          Opt[string] `json:"onboarded_at"`
	}
	if !decode(w, r, &req) {
		return
	}
	if v, ok := req.MonthStartDay.Value(); ok {
		if v < 1 || v > 28 {
			validation(w, "month_start_day must be 1-28")
			return
		}
		s.MonthStartDay = v
	}
	if v, ok := req.HideBalance.Value(); ok {
		s.HideBalance = v
	}
	if v, ok := req.HideIncome.Value(); ok {
		s.HideIncome = v
	}
	if v, ok := req.TreatTransfersAsFlow.Value(); ok {
		s.TreatTransfersAsFlow = v
	}
	if v, ok := req.Theme.Value(); ok {
		v = strings.ToLower(strings.TrimSpace(v))
		if !themes[v] {
			validation(w, "theme must be light, dark, auto or amoled")
			return
		}
		s.Theme = v
	}
	if v, ok := req.ReminderEnabled.Value(); ok {
		s.ReminderEnabled = v
	}
	if v, ok := req.ReminderTime.Value(); ok {
		if !reminderTimeRe.MatchString(v) {
			validation(w, "reminder_time must be HH:MM (24h)")
			return
		}
		s.ReminderTime = v
	}
	if req.OnboardedAt.Set {
		s.OnboardedAt = pgtype.Timestamptz{}
		if v, ok := req.OnboardedAt.Value(); ok {
			t, _, err := parseTime(v)
			if err != nil {
				validation(w, "onboarded_at: "+err.Error())
				return
			}
			s.OnboardedAt = pgtype.Timestamptz{Time: t.UTC().Truncate(time.Microsecond), Valid: true}
		}
	}
	saved, err := h.q().UpsertUserSettings(ctx, db.UpsertUserSettingsParams{
		UserID: userID, MonthStartDay: s.MonthStartDay, HideBalance: s.HideBalance, HideIncome: s.HideIncome,
		TreatTransfersAsFlow: s.TreatTransfersAsFlow, Theme: s.Theme, ReminderEnabled: s.ReminderEnabled,
		ReminderTime: s.ReminderTime, OnboardedAt: s.OnboardedAt,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settingsJSON(saved))
}

// ---------------------------------------------------------------- rate overrides

func rateJSON(base, quote string, rate float64, updated pgtype.Timestamptz) map[string]any {
	return map[string]any{"base": strings.TrimSpace(base), "quote": strings.TrimSpace(quote), "rate": rate, "updated_at": updated.Time.UTC()}
}

func (h *Handler) ListRateOverrides(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q().ListRateOverrides(r.Context(), currentUser(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, rateJSON(row.Base, row.Quote, row.Rate, row.UpdatedAt))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func ratePair(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	base, quote := normCurrency(r.PathValue("base")), normCurrency(r.PathValue("quote"))
	if !validCurrency(base) || !validCurrency(quote) {
		validation(w, "unsupported currency")
		return "", "", false
	}
	if base == quote {
		validation(w, "base and quote must differ")
		return "", "", false
	}
	return base, quote, true
}

// PutRateOverride sets a manual rate: 1 base = rate quote. It takes precedence
// over fetched rates (and its inverse is used for quote→base).
func (h *Handler) PutRateOverride(w http.ResponseWriter, r *http.Request) {
	base, quote, ok := ratePair(w, r)
	if !ok {
		return
	}
	var req struct {
		Rate float64 `json:"rate"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Rate <= 0 || req.Rate > 1e12 {
		validation(w, "rate must be > 0")
		return
	}
	row, err := h.q().UpsertRateOverride(r.Context(), db.UpsertRateOverrideParams{UserID: currentUser(r), Base: base, Quote: quote, Rate: req.Rate})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rateJSON(row.Base, row.Quote, row.Rate, row.UpdatedAt))
}

func (h *Handler) DeleteRateOverride(w http.ResponseWriter, r *http.Request) {
	base, quote, ok := ratePair(w, r)
	if !ok {
		return
	}
	n, err := h.q().DeleteRateOverride(r.Context(), db.DeleteRateOverrideParams{UserID: currentUser(r), Base: base, Quote: quote})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}
