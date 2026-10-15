package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/smtp"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	Provider    string // "brevo" | "smtp" | "log" ("" = auto-detect)
	BrevoAPIKey string
	SMTPHost    string
	SMTPPort    string
	SMTPUser    string
	SMTPPass    string
	FromEmail   string
	FromName    string
	AppURL      string
	// BrevoBaseURLs overrides the Brevo API hosts, tried in order (tests point it at a
	// fake server). Empty = the real hosts (see defaultBrevoHosts).
	BrevoBaseURLs []string
}

// defaultBrevoHosts: api.brevo.com is the canonical host; api.sendinblue.com is the
// legacy host serving the identical API — some networks/regions can't reach one of
// them, so we fall back automatically.
var defaultBrevoHosts = []string{"https://api.brevo.com", "https://api.sendinblue.com"}

type Mailer struct {
	cfg    Config
	client *http.Client
	// rawProvider is MAIL_PROVIDER as configured, before auto-detection.
	rawProvider string
}

var htmlTags = regexp.MustCompile(`(?s)<[^>]*>`)

func plainText(htmlContent string) string {
	text := html.UnescapeString(htmlTags.ReplaceAllString(htmlContent, " "))
	return strings.Join(strings.Fields(text), " ")
}

// MaskEmail hides the local part of an address for logs: "mike@gmail.com" -> "m***@gmail.com".
func MaskEmail(addr string) string {
	addr = strings.TrimSpace(addr)
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return "***"
	}
	local, domain := addr[:at], addr[at+1:]
	if local == "" {
		return "***@" + domain
	}
	r, _ := utf8.DecodeRuneInString(local)
	return string(r) + "***@" + domain
}

// FromConfig auto-detects the provider when unset:
// BREVO_API_KEY -> brevo, SMTP_HOST -> smtp, otherwise log-only (dev).
func FromConfig(cfg Config) *Mailer {
	raw := strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.Provider = raw
	if cfg.Provider == "" {
		if cfg.BrevoAPIKey != "" {
			cfg.Provider = "brevo"
		} else if cfg.SMTPHost != "" {
			cfg.Provider = "smtp"
		} else {
			cfg.Provider = "log"
		}
	}
	if cfg.FromEmail == "" {
		cfg.FromEmail = "noreply@settlr.app"
	}
	if cfg.FromName == "" {
		cfg.FromName = "Settlr"
	}
	if len(cfg.BrevoBaseURLs) == 0 {
		cfg.BrevoBaseURLs = defaultBrevoHosts
	}
	return &Mailer{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}, rawProvider: raw}
}

// Send delivers an email. It never panics and returns an error for callers
// that care; request paths should call it in a goroutine.
func (m *Mailer) Send(to, subject, html string) error {
	if to == "" {
		return fmt.Errorf("mailer: empty recipient")
	}
	switch m.cfg.Provider {
	case "brevo":
		return m.sendBrevo(to, subject, html)
	case "smtp":
		err := m.sendSMTP(to, subject, html)
		if err == nil {
			slog.Info("mailer: sent", "provider", "smtp", "subject", subject)
		}
		return err
	default:
		slog.Info("mailer: log-only provider, email not delivered", "to", MaskEmail(to), "subject", subject)
		return nil
	}
}

// SendAsync fires and forgets — use from request handlers.
func (m *Mailer) SendAsync(to, subject, html string) {
	go func() {
		if err := m.Send(to, subject, html); err != nil {
			slog.Error("mailer: send failed", "provider", m.cfg.Provider, "to", MaskEmail(to), "subject", subject, "error", err)
		}
	}()
}

// maxErrBody bounds provider error text copied into errors/logs.
const maxErrBody = 300

func (m *Mailer) sendBrevo(to, subject, html string) error {
	body := map[string]any{
		"sender":      map[string]string{"name": m.cfg.FromName, "email": m.cfg.FromEmail},
		"to":          []map[string]string{{"email": to}},
		"subject":     subject,
		"htmlContent": html,
		"textContent": plainText(html),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var lastErr error
	for _, host := range m.cfg.BrevoBaseURLs {
		req, err := http.NewRequest("POST", host+"/v3/smtp/email", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("api-key", m.cfg.BrevoAPIKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("accept", "application/json")
		resp, err := m.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			msg := strings.TrimSpace(string(raw))
			if len(msg) > maxErrBody {
				msg = msg[:maxErrBody]
			}
			lastErr = fmt.Errorf("brevo: status %d: %s", resp.StatusCode, msg)
			// 4xx (except 404) means the request itself is bad — don't retry hosts
			if resp.StatusCode != 404 {
				return lastErr
			}
			continue
		}
		// 2xx only means Brevo accepted the request; a message from an unvalidated
		// sender is rejected afterwards. The messageId lets that be looked up in
		// Brevo's transactional logs.
		var ok struct {
			MessageID string `json:"messageId"`
		}
		_ = json.Unmarshal(raw, &ok)
		slog.Info("mailer: sent", "provider", "brevo", "message_id", ok.MessageID, "subject", subject, "status", resp.StatusCode)
		return nil
	}
	return lastErr
}

// ---- Startup diagnostics ----

// diagnoseTimeout bounds the whole startup check.
const diagnoseTimeout = 8 * time.Second

// StartDiagnostics checks the mail configuration in the background. It never blocks
// startup and never fails it: problems are only logged.
func (m *Mailer) StartDiagnostics() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("mailer: diagnostics panicked", "panic", fmt.Sprint(r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), diagnoseTimeout)
		defer cancel()
		m.Diagnose(ctx)
	}()
}

// Diagnose logs the effective mail setup and, for Brevo, verifies that the
// configured sender is registered and active (Brevo silently drops messages from
// unvalidated senders after answering 201). It never logs the API key.
func (m *Mailer) Diagnose(ctx context.Context) {
	switch m.cfg.Provider {
	case "brevo":
		if m.cfg.BrevoAPIKey == "" {
			slog.Warn("mailer: MAIL_PROVIDER=brevo but BREVO_API_KEY is empty; every send will fail", "from", m.cfg.FromEmail)
			return
		}
		if m.rawProvider == "" {
			slog.Info("mailer: MAIL_PROVIDER not set; using brevo because BREVO_API_KEY is set")
		}
		m.warnPlaceholderFrom()
		m.checkBrevoSender(ctx)
	case "smtp":
		slog.Info("mailer: using SMTP", "host", m.cfg.SMTPHost, "port", m.cfg.SMTPPort, "from", m.cfg.FromEmail)
		m.warnPlaceholderFrom()
	case "log":
		if m.rawProvider == "" {
			slog.Warn("mailer: MAIL_PROVIDER is empty and no BREVO_API_KEY/SMTP_HOST is set: log-only mode, emails are NOT delivered (verification and password-reset mails will never arrive)")
		} else {
			slog.Warn("mailer: log-only mode (MAIL_PROVIDER=log): emails are NOT delivered")
		}
	default:
		slog.Warn("mailer: unknown MAIL_PROVIDER; falling back to log-only mode, emails are NOT delivered (use brevo, smtp or log)", "provider", m.cfg.Provider)
	}
}

func (m *Mailer) warnPlaceholderFrom() {
	if strings.HasSuffix(strings.ToLower(m.cfg.FromEmail), "@example.com") {
		slog.Warn("mailer: MAIL_FROM_EMAIL is a placeholder address; set a real, validated sender", "from", m.cfg.FromEmail)
	}
}

type brevoSender struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Active bool   `json:"active"`
}

// brevoGET issues an authenticated GET against the Brevo hosts (404 or a network error
// tries the next host) and returns the status and body.
func (m *Mailer) brevoGET(ctx context.Context, path string) (int, []byte, error) {
	var lastErr error
	for _, host := range m.cfg.BrevoBaseURLs {
		req, err := http.NewRequestWithContext(ctx, "GET", host+path, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("api-key", m.cfg.BrevoAPIKey)
		req.Header.Set("accept", "application/json")
		resp, err := m.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("status 404")
			continue
		}
		return resp.StatusCode, raw, nil
	}
	return 0, nil, lastErr
}

type brevoDomain struct {
	Domain        string `json:"domain_name"`
	DomainAlt     string `json:"domain"`
	Authenticated bool   `json:"authenticated"`
}

// domainAuthenticated reports whether the From address's domain is authenticated in
// Brevo (then any address on it may be used without being a listed sender).
func (m *Mailer) domainAuthenticated(ctx context.Context) (known, ok bool) {
	status, raw, err := m.brevoGET(ctx, "/v3/senders/domains")
	if err != nil || status != http.StatusOK {
		return false, false
	}
	var out struct {
		Domains []brevoDomain `json:"domains"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return false, false
	}
	at := strings.LastIndex(m.cfg.FromEmail, "@")
	if at < 0 {
		return true, false
	}
	dom := strings.ToLower(strings.TrimSpace(m.cfg.FromEmail[at+1:]))
	for _, d := range out.Domains {
		name := d.Domain
		if name == "" {
			name = d.DomainAlt
		}
		if strings.EqualFold(strings.TrimSpace(name), dom) && d.Authenticated {
			return true, true
		}
	}
	return true, false
}

func (m *Mailer) checkBrevoSender(ctx context.Context) {
	status, raw, err := m.brevoGET(ctx, "/v3/senders")
	if err != nil {
		slog.Warn("mailer: could not reach Brevo to verify the sender", "error", err)
		return
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		slog.Warn("mailer: Brevo rejected the API key (cannot list senders); emails will not be sent — check BREVO_API_KEY and its permissions", "status", status)
		return
	case status >= 300:
		slog.Warn("mailer: could not verify the Brevo sender", "status", status)
		return
	}
	var out struct {
		Senders []brevoSender `json:"senders"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		slog.Warn("mailer: could not parse Brevo sender list", "error", err)
		return
	}
	for _, s := range out.Senders {
		if !strings.EqualFold(strings.TrimSpace(s.Email), strings.TrimSpace(m.cfg.FromEmail)) {
			continue
		}
		if !s.Active {
			slog.Warn("mailer: Brevo sender exists but is NOT active; emails will be rejected — validate the sender in Brevo", "from", m.cfg.FromEmail)
			return
		}
		slog.Info("mailer: Brevo sender OK", "from", m.cfg.FromEmail)
		return
	}
	// Not listed: a domain-authenticated sender may use any address on its domain.
	known, ok := m.domainAuthenticated(ctx)
	if ok {
		slog.Info("mailer: Brevo sender OK (domain is authenticated)", "from", m.cfg.FromEmail)
		return
	}
	msg := "mailer: MAIL_FROM_EMAIL is neither a registered Brevo sender nor on an authenticated domain; emails will be rejected (Brevo still answers 201) — validate this sender in Brevo, or authenticate its domain"
	if !known {
		msg += " (the domain list could not be read, so the domain may be authenticated)"
	}
	slog.Warn(msg, "from", m.cfg.FromEmail, "registered_senders", len(out.Senders))
}

func (m *Mailer) sendSMTP(to, subject, html string) error {
	addr := m.cfg.SMTPHost + ":" + m.cfg.SMTPPort
	from := m.cfg.FromEmail
	msg := buildMessage(m.cfg.FromName, from, to, subject, html)
	var auth smtp.Auth
	if m.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPass, m.cfg.SMTPHost)
	}
	return smtp.SendMail(addr, auth, from, []string{to}, msg)
}

func buildMessage(fromName, from, to, subject, html string) []byte {
	headers := fmt.Sprintf(
		"From: %s <%s>\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=\"utf-8\"\r\n\r\n",
		fromName, from, to, subject,
	)
	return []byte(headers + html)
}

// ---- Templates ----

func wrapLayout(title, bodyHTML, ctaLabel, ctaURL string) string {
	cta := ""
	if ctaURL != "" {
		cta = fmt.Sprintf(
			`<p style="margin:28px 0 8px"><a href="%s" style="background:#14181D;color:#ffffff;text-decoration:none;padding:14px 28px;border-radius:12px;font-weight:600;display:inline-block">%s</a></p>
			 <p style="color:#8B939E;font-size:12px;word-break:break-all">Or paste this link: %s</p>`,
			ctaURL, ctaLabel, ctaURL)
	}
	return fmt.Sprintf(`<!DOCTYPE html><html><body style="margin:0;background:#F4F3EF;padding:32px 16px;font-family:-apple-system,'Segoe UI',Helvetica,Arial,sans-serif">
		<div style="max-width:520px;margin:0 auto;background:#ffffff;border-radius:16px;padding:40px;border:1px solid #E4E1D8">
			<div style="font-size:28px;font-weight:800;color:#14181D;letter-spacing:-1px">settlr<span style="color:#0C8A5F">.</span></div>
			<h1 style="font-size:22px;color:#14181D;margin:24px 0 8px">%s</h1>
			<div style="font-size:15px;line-height:1.6;color:#3f4750">%s</div>
			%s
			<p style="color:#8B939E;font-size:12px;margin-top:32px">If you didn't expect this email, you can safely ignore it.</p>
		</div>
	</body></html>`, title, bodyHTML, cta)
}

func (m *Mailer) ResetPasswordEmail(to, token string) (string, string) {
	link := m.cfg.AppURL + "/reset-password?token=" + token
	subject := "Reset your Settlr password"
	html := wrapLayout(
		"Reset your password",
		"<p>We received a request to reset your password. This link expires in <b>1 hour</b>.</p>",
		"Choose a new password",
		link,
	)
	return subject, html
}

func (m *Mailer) VerifyEmailEmail(to, token string) (string, string) {
	link := m.cfg.AppURL + "/verify-email?token=" + token
	subject := "Verify your Settlr email"
	html := wrapLayout(
		"Verify your email",
		"<p>Welcome to Settlr! Confirm this address to secure your account. The link expires in <b>24 hours</b>.</p>",
		"Verify email",
		link,
	)
	return subject, html
}

func (m *Mailer) GroupInviteEmail(to, groupName, inviterName, token string) (string, string) {
	link := m.cfg.AppURL + "/invite/" + token
	subject := inviterName + " invited you to " + groupName + " on Settlr"
	html := wrapLayout(
		"You're invited",
		fmt.Sprintf("<p><b>%s</b> invited you to the group <b>%s</b>. Accept to start splitting expenses together.</p>", inviterName, groupName),
		"Join the group",
		link,
	)
	return subject, html
}

func (m *Mailer) FriendInviteEmail(to, inviterName, token string) (string, string) {
	link := m.cfg.AppURL + "/invite/" + token
	subject := inviterName + " invited you to connect on Settlr"
	html := wrapLayout(
		"You're invited",
		fmt.Sprintf("<p><b>%s</b> invited you to become friends on Settlr. Accept to start tracking shared expenses together.</p>", inviterName),
		"Accept friend invite",
		link,
	)
	return subject, html
}
