package mailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

type Config struct {
	Provider   string // "brevo" | "smtp" | "log" ("" = auto-detect)
	BrevoAPIKey string
	SMTPHost   string
	SMTPPort   string
	SMTPUser   string
	SMTPPass   string
	FromEmail  string
	FromName   string
	AppURL     string
}

type Mailer struct {
	cfg Config
	client *http.Client
}

// FromConfig auto-detects the provider when unset:
// BREVO_API_KEY -> brevo, SMTP_HOST -> smtp, otherwise log-only (dev).
func FromConfig(cfg Config) *Mailer {
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
	return &Mailer{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (m *Mailer) Provider() string { return m.cfg.Provider }

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
		return m.sendSMTP(to, subject, html)
	default:
		slog.Info("mailer: log-only provider", "to", to, "subject", subject)
		return nil
	}
}

// SendAsync fires and forgets — use from request handlers.
func (m *Mailer) SendAsync(to, subject, html string) {
	go func() {
		if err := m.Send(to, subject, html); err != nil {
			slog.Error("mailer: send failed", "to", to, "subject", subject, "error", err)
		}
	}()
}

func (m *Mailer) sendBrevo(to, subject, html string) error {
	body := map[string]any{
		"sender":      map[string]string{"name": m.cfg.FromName, "email": m.cfg.FromEmail},
		"to":          []map[string]string{{"email": to}},
		"subject":     subject,
		"htmlContent": html,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	// api.brevo.com is the canonical host; api.sendinblue.com is the legacy
	// host serving the identical API — some networks/regions can't reach one
	// of them, so we fall back automatically.
	hosts := []string{"https://api.brevo.com", "https://api.sendinblue.com"}
	var lastErr error
	for _, host := range hosts {
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
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(resp.Body)
			lastErr = fmt.Errorf("brevo: status %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
			// 4xx (except 404) means the request itself is bad — don't retry hosts
			if resp.StatusCode != 404 {
				return lastErr
			}
			continue
		}
		return nil
	}
	return lastErr
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
