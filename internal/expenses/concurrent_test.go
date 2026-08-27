package expenses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/nabinkhanal00/settlr-api/internal/testutil"
)

func TestIntegration_ConcurrentExpenses(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()

	aliceID := registerUserViaAPI(t, srv, "AliceConc", "alice-conc@test.local", "password123")
	bobID := registerUserViaAPI(t, srv, "BobConc", "bob-conc@test.local", "password123")
	aliceTok := tokenFor(aliceID)

	body, _ := json.Marshal(map[string]string{"name": "Conc Group"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	var groupOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&groupOut)
	resp.Body.Close()
	groupID := groupOut["id"].(string)

	addBody, _ := json.Marshal(map[string]string{"user_id": bobID.String()})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/members", bytes.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	// Concurrently create 10 expenses
	var wg sync.WaitGroup
	errCh := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			expBody, _ := json.Marshal(map[string]any{
				"description": fmt.Sprintf("Expense %d", idx),
				"amount":      1000,
				"paid_by":     aliceID.String(),
				"split_mode":  "EQUAL",
				"splits": []map[string]string{
					{"user_id": aliceID.String()},
					{"user_id": bobID.String()},
				},
			})
			req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/expenses", bytes.NewReader(expBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+aliceTok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errCh <- err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 201 {
				var errBody map[string]any
				_ = json.NewDecoder(resp.Body).Decode(&errBody)
				errCh <- fmt.Errorf("status %d %v", resp.StatusCode, errBody)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent create failed: %v", err)
	}

	// Verify balances: 10 * 1000 = 10000 total, each expense 500 each, so Alice paid 10000, owed 5000, net +5000, Bob -5000
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+groupID+"/balances", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var balOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&balOut)
	resp.Body.Close()
	data := balOut["data"].([]any)
	m := make(map[string]int64)
	for _, item := range data {
		mm := item.(map[string]any)
		m[mm["user_id"].(string)] = int64(mm["amount"].(float64))
	}
	if m[aliceID.String()] != 5000 || m[bobID.String()] != -5000 {
		t.Fatalf("concurrent balances %v", m)
	}
	var sum int64
	for _, v := range m {
		sum += v
	}
	if sum != 0 {
		t.Fatalf("sum not zero %d", sum)
	}
}
