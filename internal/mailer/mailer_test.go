package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "xkeysib-SUPER-SECRET-KEY-123"

// captureLogs routes slog output to a buffer for the duration of the test.
func captureLogs(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeBrevo is a minimal Brevo API.
type fakeBrevo struct {
	srv         *httptest.Server
	mu          sync.Mutex
	sendStatus  int
	sendBody    string
	senders     string
	sendersCode int
	sent        []map[string]any
	keys        []string
	sendersHits int
	domains     string
	domainsCode int
}

func newFakeBrevo(t *testing.T) *fakeBrevo {
	t.Helper()
	f := &fakeBrevo{domains: `{"domains":[]}`, domainsCode: 200, sendStatus: 201, sendBody: `{"messageId":"<202610150000.12345@smtp-relay.mailin.fr>"}`, sendersCode: 200,
		senders: `{"senders":[{"id":1,"name":"Settlr","email":"mail@settlr.app","active":true,"ips":[]}]}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.keys = append(f.keys, r.Header.Get("api-key"))
		switch {
		case r.Method == "POST" && r.URL.Path == "/v3/smtp/email":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			f.sent = append(f.sent, m)
			w.WriteHeader(f.sendStatus)
			_, _ = io.WriteString(w, f.sendBody)
		case r.Method == "GET" && r.URL.Path == "/v3/senders":
			f.sendersHits++
			w.WriteHeader(f.sendersCode)
			_, _ = io.WriteString(w, f.senders)
		case r.Method == "GET" && r.URL.Path == "/v3/senders/domains":
			w.WriteHeader(f.domainsCode)
			_, _ = io.WriteString(w, f.domains)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBrevo) mailer(from string) *Mailer {
	return FromConfig(Config{Provider: "brevo", BrevoAPIKey: testKey, FromEmail: from, FromName: "Settlr", BrevoBaseURLs: []string{f.srv.URL}})
}

func TestMaskEmail(t *testing.T) {
	for in, want := range map[string]string{
		"mike@gmail.com": "m***@gmail.com", "a@b.co": "a***@b.co", "@x.com": "***@x.com", "nope": "***", "": "***",
		"Élodie@x.fr": "É***@x.fr", " spaced@x.com ": "s***@x.com",
	} {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBrevoSendLogsMessageIDWithoutRecipientOrKey(t *testing.T) {
	logs := captureLogs(t)
	f := newFakeBrevo(t)
	m := f.mailer("mail@settlr.app")
	if err := m.Send("mike@gmail.com", "Verify your Settlr email", "<p>Hi</p>"); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || f.keys[0] != testKey {
		t.Fatalf("request not sent with api-key: %v %v", f.sent, f.keys)
	}
	to := f.sent[0]["to"].([]any)[0].(map[string]any)["email"]
	if to != "mike@gmail.com" || f.sent[0]["sender"].(map[string]any)["email"] != "mail@settlr.app" || f.sent[0]["subject"] != "Verify your Settlr email" {
		t.Fatalf("payload: %v", f.sent[0])
	}
	out := logs.String()
	for _, want := range []string{"level=INFO", "mailer: sent", "provider=brevo", "202610150000.12345@smtp-relay.mailin.fr", `subject="Verify your Settlr email"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"mike@gmail.com", "mike", testKey} {
		if strings.Contains(out, bad) {
			t.Errorf("log leaks %q:\n%s", bad, out)
		}
	}
}

func TestBrevoSendFailureReturnsErrorAndMasksRecipient(t *testing.T) {
	logs := captureLogs(t)
	f := newFakeBrevo(t)
	f.sendStatus, f.sendBody = 400, `{"code":"invalid_parameter","message":"bad sender"}`
	m := f.mailer("mail@settlr.app")
	err := m.Send("mike@gmail.com", "Hello", "<p>x</p>")
	if err == nil || !strings.Contains(err.Error(), "status 400") || !strings.Contains(err.Error(), "bad sender") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatal("error leaks key")
	}
	if len(f.sent) != 1 {
		t.Fatalf("400 must not be retried: %d", len(f.sent))
	}
	// SendAsync logs failures with the masked recipient.
	m.SendAsync("mike@gmail.com", "Hello", "<p>x</p>")
	waitFor(t, func() bool { return strings.Contains(logs.String(), "mailer: send failed") })
	out := logs.String()
	if !strings.Contains(out, "m***@gmail.com") || strings.Contains(out, "mike@gmail.com") || strings.Contains(out, testKey) {
		t.Fatalf("failure log:\n%s", out)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestBrevoFallsBackToSecondHostOn404(t *testing.T) {
	logs := captureLogs(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	defer dead.Close()
	f := newFakeBrevo(t)
	m := FromConfig(Config{Provider: "brevo", BrevoAPIKey: testKey, FromEmail: "mail@settlr.app", BrevoBaseURLs: []string{dead.URL, f.srv.URL}})
	if err := m.Send("a@b.co", "S", "<p>x</p>"); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || !strings.Contains(logs.String(), "mailer: sent") {
		t.Fatalf("fallback failed: %d\n%s", len(f.sent), logs.String())
	}
}

func TestDefaultsKeepRealHosts(t *testing.T) {
	m := FromConfig(Config{Provider: "brevo", BrevoAPIKey: "k"})
	if len(m.cfg.BrevoBaseURLs) != 2 || m.cfg.BrevoBaseURLs[0] != "https://api.brevo.com" || m.cfg.BrevoBaseURLs[1] != "https://api.sendinblue.com" {
		t.Fatalf("default hosts: %v", m.cfg.BrevoBaseURLs)
	}
}

func TestDiagnoseBrevoSender(t *testing.T) {
	cases := []struct {
		name     string
		from     string
		senders  string
		code     int
		wantLvl  string
		wantText string
	}{
		{"ok", "mail@settlr.app", "", 200, "level=INFO", "Brevo sender OK"},
		{"ok case-insensitive", "Mail@Settlr.App", "", 200, "level=INFO", "Brevo sender OK"},
		{"missing", "noreply@other.com", "", 200, "level=WARN", "emails will be rejected"},
		{"inactive", "mail@settlr.app", `{"senders":[{"id":1,"email":"mail@settlr.app","active":false}]}`, 200, "level=WARN", "NOT active"},
		{"empty list", "mail@settlr.app", `{"senders":[]}`, 200, "level=WARN", "emails will be rejected"},
		{"bad key", "mail@settlr.app", `{"message":"Key not found"}`, 401, "level=WARN", "rejected the API key"},
		{"server error", "mail@settlr.app", `oops`, 500, "level=WARN", "could not verify"},
		{"garbage", "mail@settlr.app", `not json`, 200, "level=WARN", "could not parse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			f := newFakeBrevo(t)
			f.sendersCode = tc.code
			if tc.senders != "" {
				f.senders = tc.senders
			}
			f.mailer(tc.from).Diagnose(context.Background())
			out := logs.String()
			if !strings.Contains(out, tc.wantLvl) || !strings.Contains(out, tc.wantText) {
				t.Fatalf("want %s %q:\n%s", tc.wantLvl, tc.wantText, out)
			}
			if strings.Contains(out, testKey) {
				t.Fatalf("diagnostics leak the API key:\n%s", out)
			}
			if f.sendersHits != 1 || len(f.sent) != 0 {
				t.Fatalf("hits=%d sent=%d", f.sendersHits, len(f.sent))
			}
			if f.keys[0] != testKey {
				t.Fatal("api-key header not sent")
			}
		})
	}
}

func TestDiagnoseDomainAuthenticatedSenderIsOK(t *testing.T) {
	for _, body := range []string{
		`{"domains":[{"domain_name":"settlr.app","authenticated":true,"verified":true}]}`,
		`{"domains":[{"domain":"SETTLR.app","authenticated":true}]}`,
	} {
		logs := captureLogs(t)
		f := newFakeBrevo(t)
		f.domains = body
		f.mailer("billing@settlr.app").Diagnose(context.Background()) // not in the sender list
		out := logs.String()
		if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "domain is authenticated") || strings.Contains(out, "level=WARN") {
			t.Fatalf("log:\n%s", out)
		}
	}
	// Domain present but not authenticated, other domain only, or unreadable list: warn.
	for name, tc := range map[string]struct {
		body string
		code int
		hint string
	}{
		"unauthenticated": {`{"domains":[{"domain_name":"settlr.app","authenticated":false}]}`, 200, "authenticate its domain"},
		"other domain":    {`{"domains":[{"domain_name":"other.com","authenticated":true}]}`, 200, "validate this sender"},
		"list failed":     {`oops`, 500, "may be authenticated"},
	} {
		logs := captureLogs(t)
		f := newFakeBrevo(t)
		f.domains, f.domainsCode = tc.body, tc.code
		f.mailer("billing@settlr.app").Diagnose(context.Background())
		out := logs.String()
		if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "emails will be rejected") || !strings.Contains(out, tc.hint) {
			t.Fatalf("%s:\n%s", name, out)
		}
	}
}

func TestDiagnoseUnreachableBrevoWarnsAndDoesNotPanic(t *testing.T) {
	logs := captureLogs(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	m := FromConfig(Config{Provider: "brevo", BrevoAPIKey: testKey, FromEmail: "mail@settlr.app", BrevoBaseURLs: []string{url}})
	m.Diagnose(context.Background())
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "could not reach Brevo") || strings.Contains(out, testKey) {
		t.Fatalf("log:\n%s", out)
	}
}

func TestDiagnoseMisconfiguration(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"nothing configured", Config{}, "log-only mode, emails are NOT delivered"},
		{"unknown provider", Config{Provider: "sendgrid"}, "unknown MAIL_PROVIDER"},
		{"explicit log", Config{Provider: "log"}, "MAIL_PROVIDER=log"},
		{"brevo without key", Config{Provider: "brevo", FromEmail: "mail@settlr.app"}, "BREVO_API_KEY is empty"},
		{"placeholder sender", Config{Provider: "smtp", SMTPHost: "h", FromEmail: "mail@example.com"}, "placeholder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			FromConfig(tc.cfg).Diagnose(context.Background())
			out := logs.String()
			if !strings.Contains(out, "level=WARN") || !strings.Contains(out, tc.want) {
				t.Fatalf("want WARN %q:\n%s", tc.want, out)
			}
		})
	}
	// A configured, non-placeholder SMTP setup is quiet at WARN level.
	logs := captureLogs(t)
	FromConfig(Config{Provider: "smtp", SMTPHost: "smtp.x", FromEmail: "mail@settlr.app"}).Diagnose(context.Background())
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("unexpected warning:\n%s", logs.String())
	}
}

func TestStartDiagnosticsIsNonBlockingAndNeverFatal(t *testing.T) {
	logs := captureLogs(t)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	m := FromConfig(Config{Provider: "brevo", BrevoAPIKey: testKey, FromEmail: "mail@settlr.app", BrevoBaseURLs: []string{srv.URL}})
	done := make(chan struct{})
	go func() { m.StartDiagnostics(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartDiagnostics blocked")
	}
	_ = logs
}

func waitForQuiet() {
	ctx, cancel := context.WithTimeout(context.Background(), 2_000_000_000)
	defer cancel()
	<-ctx.Done()
}

func TestLogOnlySendMasksRecipient(t *testing.T) {
	logs := captureLogs(t)
	if err := FromConfig(Config{}).Send("mike@gmail.com", "Hi", "<p>x</p>"); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if !strings.Contains(out, "m***@gmail.com") || strings.Contains(out, "mike@gmail.com") {
		t.Fatalf("log:\n%s", out)
	}
}
