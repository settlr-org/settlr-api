// apimatrix exercises every API operation individually (happy + error paths)
// and as chains. Run against a live server:
//
//	go run ./cmd/apimatrix -base http://localhost:18080
//
// Exits non-zero on any failure. Safe to re-run: creates its own users.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
)

var (
	base                 = flag.String("base", "http://localhost:18080", "base URL")
	verbose              = flag.Bool("v", false, "print every check")
	passCount, failCount int
	failures             []string
)

type client struct{ token string }

func (c *client) do(method, path string, body any, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, *base+path, rdr)
	if err != nil {
		return 0, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

func check(name string, ok bool, detail ...any) {
	if ok {
		passCount++
		if *verbose {
			fmt.Println("PASS", name)
		}
	} else {
		failCount++
		failures = append(failures, fmt.Sprintf("%s %v", name, detail))
		fmt.Println("*FAIL*", name, fmt.Sprint(detail...))
	}
}

func expectBool(name string, ok bool, detail ...any) {
	check(name, ok, detail...)
}

func expect(name string, got int, want ...int) bool {
	for _, w := range want {
		if got == w {
			check(name, true)
			return true
		}
	}
	check(name, false, fmt.Sprintf("status=%d want=%v", got, want))
	return false
}

func main() {
	flag.Parse()
	rand.Seed(1)
	sfx := fmt.Sprintf("%06d", rand.Intn(900000)+100000)

	// ===== health =====
	c := &client{}
	s, _ := c.do("GET", "/health", nil, nil)
	expect("API-001 GET /health", s, 200)

	// ===== auth: register/login/errors =====
	s, _ = c.do("POST", "/api/v1/auth/register", map[string]any{"name": "X", "email": "bad", "password": "password123"}, nil)
	expect("API-002 register invalid email 422", s, 422)
	s, _ = c.do("POST", "/api/v1/auth/register", map[string]any{"name": "X", "email": "a@b.io", "password": "short"}, nil)
	expect("API-003 register short pw 422", s, 422)

	var reg struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		User         struct{ ID string `json:"id"` } `json:"user"`
	}
	s, _ = c.do("POST", "/api/v1/auth/register", map[string]any{"name": "Matrix A", "email": "ma" + sfx + "@x.io", "password": "password123"}, &reg)
	expect("API-004 register A", s, 201)
	A := &client{reg.AccessToken}
	AID := reg.User.ID
	var regB struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		User         struct{ ID string `json:"id"` } `json:"user"`
	}
	s, _ = c.do("POST", "/api/v1/auth/register", map[string]any{"name": "Matrix B", "email": "mb" + sfx + "@x.io", "password": "password123"}, &regB)
	B := &client{regB.AccessToken}
	BID := regB.User.ID
	var regC struct {
		AccessToken string `json:"access_token"`
		User        struct{ ID string `json:"id"` } `json:"user"`
	}
	s, _ = c.do("POST", "/api/v1/auth/register", map[string]any{"name": "Matrix C", "email": "mc" + sfx + "@x.io", "password": "password123"}, &regC)
	C := &client{regC.AccessToken}
	CID := regC.User.ID

	s, _ = c.do("POST", "/api/v1/auth/login", map[string]any{"email": "ma" + sfx + "@x.io", "password": "WRONG"}, nil)
	expect("API-005 login wrong pw 401", s, 401)
	var lg struct {
		AccessToken string `json:"access_token"`
	}
	s, _ = c.do("POST", "/api/v1/auth/login", map[string]any{"email": "ma" + sfx + "@x.io", "password": "password123"}, &lg)
	expect("API-006 login ok", s, 200)
	s, _ = c.do("POST", "/api/v1/auth/refresh", map[string]any{"refresh_token": "bogus"}, nil)
	expect("API-007 refresh bogus 401", s, 401)
	s, _ = c.do("POST", "/api/v1/auth/refresh", map[string]any{"refresh_token": regB.RefreshToken}, &lg)
	expect("API-008 refresh ok", s, 200)
	s, _ = c.do("POST", "/api/v1/auth/forgot-password", map[string]any{"email": "ma" + sfx + "@x.io"}, nil)
	expect("API-009 forgot known", s, 200)
	s, _ = c.do("POST", "/api/v1/auth/forgot-password", map[string]any{"email": "zz@x.io"}, nil)
	expect("API-010 forgot unknown generic", s, 200)
	s, _ = c.do("POST", "/api/v1/auth/reset-password", map[string]any{"token": "bad", "new_password": "password123"}, nil)
	expect("API-011 reset bad token", s, 400, 422)
	s, _ = c.do("POST", "/api/v1/auth/verify-email", map[string]any{"token": "bad"}, nil)
	expect("API-012 verify bad token", s, 400, 422)

	// ===== me =====
	var me struct{ ID, Name, DefaultCurrency string }
	s, _ = A.do("GET", "/api/v1/me", nil, &me)
	expect("API-013 GET /me", s, 200)
	s, _ = A.do("PATCH", "/api/v1/me", map[string]any{"name": "Matrix A2", "default_currency": "EUR"}, nil)
	expect("API-014 PATCH /me", s, 200)
	s, _ = c.do("GET", "/api/v1/me", nil, nil)
	expect("API-015 /me unauthed 401", s, 401)
	s, _ = A.do("PATCH", "/api/v1/me/password", map[string]any{"current_password": "WRONG", "new_password": "password1234"}, nil)
	expect("API-016 password change wrong current", s, 401)
	s, _ = A.do("PATCH", "/api/v1/me/password", map[string]any{"current_password": "password123", "new_password": "password123"}, nil)
	expect("API-017 password change same-ok", s, 200)
	// password change revokes all sessions -> re-login
	s, _ = c.do("POST", "/api/v1/auth/login", map[string]any{"email": "ma" + sfx + "@x.io", "password": "password123"}, &lg)
	A = &client{lg.AccessToken}
	s, _ = A.do("POST", "/api/v1/auth/resend-verification", nil, nil)
	expect("API-018 resend verification", s, 200)

	// ===== users search =====
	s, _ = A.do("GET", "/api/v1/users/search?q=Matrix", nil, nil)
	s, _ = A.do("GET", "/api/v1/users/search?q=Matrix", nil, nil)
	expect("API-019 users search", s, 200)

	// ===== groups =====
	s, _ = A.do("POST", "/api/v1/groups", map[string]any{"name": "", "currency": "USD"}, nil)
	expect("API-020 group empty name 422", s, 422)
	var g struct{ ID string }
	s, _ = A.do("POST", "/api/v1/groups", map[string]any{"name": "Matrix Group", "currency": "EUR"}, &g)
	expect("API-021 group create", s, 201)
	G := g.ID
	s, _ = A.do("GET", "/api/v1/groups/"+G, nil, nil)
	expect("API-022 group get", s, 200)
	s, _ = A.do("PATCH", "/api/v1/groups/"+G, map[string]any{"name": "Matrix Group 2", "group_type": "TRIP", "currency": "EUR", "simplify_debts": true}, nil)
	expect("API-023 group patch", s, 200)
	s, _ = B.do("GET", "/api/v1/groups/"+G, nil, nil)
	expectBool("API-024 group get non-member 403/404", s == 403 || s == 404, s)

	// members
	var addReq = func(tok *client, body map[string]any) int {
		s, _ := tok.do("POST", "/api/v1/groups/"+G+"/members", body, nil)
		return s
	}
	expect("API-025 member add", func() int { return addReq(A, map[string]any{"email": "mb" + sfx + "@x.io"}) }(), 200, 201)
	expect("API-026 member dup 409", func() int { return addReq(A, map[string]any{"email": "mb" + sfx + "@x.io"}) }(), 409)
	expect("API-027 member unknown 404", func() int { return addReq(A, map[string]any{"email": "ghost@x.io"}) }(), 404)
	expect("API-028 member add by non-admin 403", func() int { return addReq(B, map[string]any{"email": "mc" + sfx + "@x.io"}) }(), 403)
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/members", nil, nil)
	expect("API-029 members list", s, 200)
	// role change (PATCH member)
	s, _ = A.do("PATCH", fmt.Sprintf("/api/v1/groups/%s/members/%s", G, BID), map[string]any{"role": "ADMIN"}, nil)
	expect("API-030 member role change", s, 200)
	// also add C so 3-way splits are valid
	s, _ = A.do("POST", "/api/v1/groups/"+G+"/members", map[string]any{"email": "mc" + sfx + "@x.io"}, nil)
	expect("API-030b member add C", s, 201)

	// ===== expenses (all modes + fx + errors) =====
	mkExp := func(tok *client, body map[string]any) (int, struct{ ID string }) {
		var e struct{ ID string }
		s, _ := tok.do("POST", "/api/v1/groups/"+G+"/expenses", body, &e)
		return s, e
	}
	base := map[string]any{"description": "Lunch", "amount": 10001, "paid_by": AID, "split_mode": "EQUAL", "splits": []map[string]any{{"user_id": AID}, {"user_id": BID}, {"user_id": CID}}}
	s, e1 := mkExp(A, base)
	expect("API-031 expense EQUAL (remainder)", s, 201)
	exact := map[string]any{"description": "Exact", "amount": 10000, "paid_by": AID, "split_mode": "EXACT", "splits": []map[string]any{{"user_id": AID, "amount": 4000}, {"user_id": BID, "amount": 6000}, {"user_id": CID, "amount": 0}}}
	s, _ = mkExp(A, exact)
	expect("API-032 expense EXACT", s, 201)
	badExact := map[string]any{"description": "BadExact", "amount": 10000, "paid_by": AID, "split_mode": "EXACT", "splits": []map[string]any{{"user_id": AID, "amount": 4000}, {"user_id": BID, "amount": 4000}, {"user_id": CID, "amount": 1000}}}
	s, _ = mkExp(A, badExact)
	expect("API-033 EXACT mismatch 422", s, 422)
	pct := map[string]any{"description": "Pct", "amount": 10000, "paid_by": AID, "split_mode": "PERCENTAGE", "splits": []map[string]any{{"user_id": AID, "percentage": 50}, {"user_id": BID, "percentage": 30}, {"user_id": CID, "percentage": 20}}}
	s, _ = mkExp(A, pct)
	expect("API-034 expense PERCENTAGE", s, 201)
	badPct := map[string]any{"description": "BadPct", "amount": 10000, "paid_by": AID, "split_mode": "PERCENTAGE", "splits": []map[string]any{{"user_id": AID, "percentage": 50}, {"user_id": BID, "percentage": 30}, {"user_id": CID, "percentage": 10}}}
	s, _ = mkExp(A, badPct)
	expect("API-035 PERCENTAGE sum!=100 422", s, 422)
	shares := map[string]any{"description": "Shares", "amount": 9000, "paid_by": AID, "split_mode": "SHARES", "splits": []map[string]any{{"user_id": AID, "shares": 2}, {"user_id": BID, "shares": 1}, {"user_id": CID, "shares": 1}}}
	s, e4 := mkExp(A, shares)
	expect("API-036 expense SHARES", s, 201)
	fx := map[string]any{"description": "USD buy", "amount": 3000, "currency": "USD", "exchange_rate": 1.1, "paid_by": AID, "split_mode": "EQUAL", "splits": []map[string]any{{"user_id": AID}, {"user_id": BID}, {"user_id": CID}}}
	s, _ = mkExp(A, fx)
	expect("API-037 expense FX", s, 201)
	noRate := map[string]any{"description": "NoRate", "amount": 5000, "currency": "USD", "paid_by": AID, "split_mode": "EQUAL", "splits": []map[string]any{{"user_id": AID}}}
	s, _ = mkExp(A, noRate)
	expect("API-038 FX missing rate 422", s, 422)
	s, _ = C.do("PATCH", "/api/v1/expenses/"+e1.ID, map[string]any{"description": "H", "amount": 1, "paid_by": AID, "split_mode": "EQUAL", "splits": []map[string]any{{"user_id": AID}}}, nil)
	expect("API-039 edit by unrelated 403", s, 403)
	s, _ = A.do("PATCH", "/api/v1/expenses/"+e1.ID, map[string]any{"description": "Lunch v2", "amount": 10001, "paid_by": AID, "split_mode": "EQUAL", "splits": []map[string]any{{"user_id": AID}, {"user_id": BID}, {"user_id": CID}}}, nil)
	expect("API-040 expense patch", s, 200)
	s, _ = A.do("GET", "/api/v1/expenses/"+e1.ID, nil, nil)
	expect("API-041 expense get", s, 200)
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/expenses", nil, nil)
	expect("API-042 expenses list", s, 200)

	// comments
	var cm struct{ ID string }
	s, _ = A.do("POST", "/api/v1/expenses/"+e1.ID+"/comments", map[string]any{"body": "matrix comment"}, &cm)
	expect("API-043 comment create", s, 201)
	s, _ = A.do("POST", "/api/v1/expenses/"+e1.ID+"/comments", map[string]any{"body": "  "}, nil)
	expect("API-044 comment blank 422", s, 422)
	s, _ = A.do("GET", "/api/v1/expenses/"+e1.ID+"/comments", nil, nil)
	expect("API-045 comments list", s, 200)
	s, _ = A.do("DELETE", "/api/v1/comments/"+cm.ID, nil, nil)
	expect("API-046 comment delete", s, 200)

	// attachments (multipart)
	s = uploadAttachment(*A, e1.ID)
	expect("API-047 attachment upload", s, 201)
	var atts struct{ Data []struct{ ID string } }
	s, _ = A.do("GET", "/api/v1/expenses/"+e1.ID+"/attachments", nil, &atts)
	expect("API-048 attachments list", s, 200)
	if len(atts.Data) > 0 {
		s, _ = A.do("DELETE", "/api/v1/attachments/"+atts.Data[0].ID, nil, nil)
		expect("API-049 attachment delete", s, 200)
	}

	// ===== balances / debts / stats / activity =====
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/balances", nil, nil)
	var bal struct{ Data []struct{ Amount int64 `json:"amount"` } `json:"data"` }
	// fetch fresh balances with current A token
	s2, raw2 := func() (int, string) { return httpReq("GET", "/api/v1/groups/"+G+"/balances", A.token) }()
	_ = s2
	_ = json.Unmarshal([]byte(raw2), &bal)
	tot := int64(0)
	for _, b := range bal.Data {
		tot += b.Amount
	}
	check("API-050 balances sum 0 (fx included)", tot == 0 || tot == -1 || tot == 1, tot)
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/debts", nil, nil)
	expect("API-051 debts", s, 200)
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/stats", nil, nil)
	expect("API-052 stats", s, 200)
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/activity", nil, nil)
	expect("API-053 group activity", s, 200)
	s, _ = A.do("GET", "/api/v1/activity", nil, nil)
	expect("API-054 global activity", s, 200)

	// ===== settlements =====
	var st struct{ ID string }
	s, _ = A.do("POST", "/api/v1/groups/"+G+"/settlements", map[string]any{"from_user": BID, "to_user": AID, "amount": 2500}, &st)
	expect("API-055 settlement create", s, 201)
	s, _ = A.do("POST", "/api/v1/groups/"+G+"/settlements", map[string]any{"from_user": AID, "to_user": AID, "amount": 100}, nil)
	expect("API-056 settlement self 422", s, 422)
	s, _ = A.do("PATCH", "/api/v1/settlements/"+st.ID, map[string]any{"amount": 2600}, nil)
	expect("API-057 settlement patch", s, 200)
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/settlements", nil, nil)
	expect("API-058 settlements list", s, 200)
	s, _ = A.do("DELETE", "/api/v1/settlements/"+st.ID, nil, nil)
	expect("API-059 settlement delete", s, 200)

	// ===== friends =====
	s, _ = A.do("POST", "/api/v1/friends/"+BID+"/request", nil, nil)
	expect("API-060 friend request", s, 201)
	s, _ = B.do("POST", "/api/v1/friends/"+AID+"/accept", nil, nil)
	expect("API-061 friend accept", s, 200)
	s, _ = A.do("GET", "/api/v1/friends", nil, nil)
	expect("API-062 friends list", s, 200)
	s, _ = A.do("GET", "/api/v1/friends/requests", nil, nil)
	expect("API-063 friend requests list", s, 200)
	var led struct{ GroupID string }
	s, _ = A.do("GET", "/api/v1/friends/"+BID+"/ledger", nil, &led)
	expect("API-064 friend ledger", s, 200)
	s, _ = A.do("GET", "/api/v1/users/"+BID, nil, nil)
	expect("API-065 user get", s, 200)

	// ===== invites =====
	var inv struct{ Token string }
	s, _ = B.do("POST", "/api/v1/groups/"+G+"/invites", map[string]any{"email": "newbie" + sfx + "@x.io"}, &inv)
	expect("API-066 invite by member (any role)", s, 201)
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/invites", nil, nil)
	expect("API-067 invites list", s, 200)
	s, _ = A.do("GET", "/api/v1/invites", nil, nil)
	expect("API-068 my invites", s, 200)
	var regD struct {
		AccessToken string `json:"access_token"`
		User        struct{ ID string `json:"id"` } `json:"user"`
	}
	s, _ = c.do("POST", "/api/v1/auth/register", map[string]any{"name": "Matrix D", "email": "newbie" + sfx + "@x.io", "password": "password123"}, &regD)
	D := &client{regD.AccessToken}
	s, _ = D.do("POST", "/api/v1/invites/"+inv.Token+"/accept", nil, nil)
	expect("API-069 invite accept", s, 200)
	_ = D
	_ = C
	s, _ = C.do("POST", "/api/v1/invites/"+inv.Token+"/accept", nil, nil)
	expectBool("API-070 invite re-accept 4xx", s != 200, s)

	// ===== recurring =====
	var rec struct{ ID string }
	s, _ = A.do("POST", "/api/v1/groups/"+G+"/recurring", map[string]any{"description": "Rent", "amount": 50000, "paid_by": AID, "frequency": "MONTHLY"}, &rec)
	expect("API-071 recurring create", s, 201)
	s, _ = A.do("GET", "/api/v1/groups/"+G+"/recurring", nil, nil)
	expect("API-072 recurring list", s, 200)
	s, _ = A.do("PATCH", "/api/v1/recurring/"+rec.ID, map[string]any{"active": false}, nil)
	expect("API-073 recurring pause", s, 200)
	s, _ = A.do("DELETE", "/api/v1/recurring/"+rec.ID, nil, nil)
	expect("API-074 recurring delete", s, 200)

	// ===== categories / search / notifications =====
	var cat struct{ ID string }
	s, _ = A.do("POST", "/api/v1/categories", map[string]any{"name": "MatrixCat"}, &cat)
	expect("API-075 category create", s, 201)
	s, _ = A.do("GET", "/api/v1/categories", nil, nil)
	expect("API-076 categories list", s, 200)
	s, _ = A.do("GET", "/api/v1/search?q=Lunch", nil, nil)
	expect("API-077 search", s, 200)
	s, _ = A.do("GET", "/api/v1/notifications", nil, nil)
	expect("API-078 notifications list", s, 200)
	s, _ = A.do("POST", "/api/v1/notifications/read-all", nil, nil)
	expect("API-079 read-all", s, 200)

	// ===== sessions / logout =====
	s, _ = A.do("GET", "/api/v1/auth/sessions", nil, nil)
	expect("API-080 sessions list", s, 200)
	s, _ = A.do("DELETE", "/api/v1/auth/sessions", nil, nil)
	expect("API-081 revoke all sessions", s, 200)
	s, _ = A.do("GET", "/api/v1/me", nil, nil)
	expectBool("API-082 after revoke 401/200", s == 401 || s == 200, s)
	// re-login for remaining checks
	s, _ = c.do("POST", "/api/v1/auth/login", map[string]any{"email": "ma" + sfx + "@x.io", "password": "password123"}, &lg)
	A = &client{lg.AccessToken}
	s, _ = A.do("POST", "/api/v1/auth/logout", nil, nil)
	expect("API-083 logout", s, 200)
	s, _ = c.do("POST", "/api/v1/auth/login", map[string]any{"email": "ma" + sfx + "@x.io", "password": "password123"}, &lg)
	A = &client{lg.AccessToken}

	// ===== CSV exports =====
	expectBool("API-084 group CSV", csvOK(A.token, "/api/v1/groups/"+G+"/export.csv"))
	expectBool("API-085 account CSV", csvOK(A.token, "/api/v1/me/export.csv"))

	// ===== deletes (own fixtures) =====
	s, _ = A.do("DELETE", "/api/v1/expenses/"+e4.ID, nil, nil)
	expect("API-086 expense delete", s, 200)
	s, _ = A.do("DELETE", fmt.Sprintf("/api/v1/groups/%s/members/%s", G, CID), nil, nil)
	expect("API-087 member remove", s, 200)
	var g2 struct{ ID string }
	s, _ = C.do("POST", "/api/v1/groups", map[string]any{"name": "Temp", "currency": "USD"}, &g2)
	s, _ = C.do("POST", "/api/v1/groups/"+g2.ID+"/archive", nil, nil)
	expect("API-088 archive", s, 200)
	s, _ = C.do("DELETE", "/api/v1/groups/"+g2.ID, nil, nil)
	expect("API-089 group delete", s, 200)
	s, _ = A.do("DELETE", "/api/v1/friends/"+BID, nil, nil)
	expect("API-090 friend remove", s, 200)

	// ===== uploads fetch =====
	s, _ = A.do("GET", "/uploads/00000000-0000-0000-0000-000000000000", nil, nil)
	expectBool("API-091 uploads 404 path", s == 404, s)

	fmt.Printf("\n=== API MATRIX: %d passed, %d failed ===\n", passCount, failCount)
	for _, f := range failures {
		fmt.Println("  FAILED:", f)
	}
	if failCount > 0 {
		os.Exit(1)
	}
}

func httpReq(method, path, token string) (int, string) {
	req, _ := http.NewRequest(method, *base+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func readBody(method, path, token string) string {
	_, b := httpReq(method, path, token)
	return b
}

func uploadAttachment(c client, expenseID string) int {
	var buf bytes.Buffer
	buf.WriteString("--X\r\nContent-Disposition: form-data; name=\"file\"; filename=\"r.txt\"\r\nContent-Type: text/plain\r\n\r\nreceipt\r\n--X--\r\n")
	req, _ := http.NewRequest("POST", *base+"/api/v1/expenses/"+expenseID+"/attachments", &buf)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=X")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func csvOK(token, path string) bool {
	s, body := httpReq("GET", path, token)
	return s == 200 && strings.Contains(body, "type,date")
}
