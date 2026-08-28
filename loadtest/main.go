// Command loadtest drives a running ledger-service instance with concurrent
// transfers, reports throughput and latency percentiles, and verifies that
// total balance is conserved under load.
//
//	go run ./loadtest -addr http://localhost:8080 -accounts 50 -workers 64 -duration 30s
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	addr := flag.String("addr", "http://localhost:8080", "base URL of a running ledger-service")
	nAccounts := flag.Int("accounts", 50, "number of accounts to spread transfers across")
	nWorkers := flag.Int("workers", 64, "concurrent request workers")
	duration := flag.Duration("duration", 30*time.Second, "how long to sustain load")
	amount := flag.Int64("amount", 100, "minor units per transfer")
	seed := flag.Int64("seed", 1_000_000, "initial balance per account (minor units)")
	flag.Parse()

	c := &client{base: *addr, http: &http.Client{Timeout: 15 * time.Second}}

	fmt.Printf("• seeding %d accounts (balance %d each)\n", *nAccounts, *seed)
	accounts := make([]string, *nAccounts)
	for i := range accounts {
		id, err := c.createAccount("USD", false)
		must(err)
		accounts[i] = id
	}
	// Fund every account from an overdraft-enabled treasury.
	treasury, err := c.createAccount("USD", true)
	must(err)
	for _, a := range accounts {
		must(c.transfer(randKey(), treasury, a, *seed, "USD"))
	}
	startTotal := c.sumBalances(append(accounts, treasury))

	fmt.Printf("• running %d workers for %s\n\n", *nWorkers, *duration)
	var (
		ok, conflict, insufficient, failed int64
		lat                                = &latencies{}
		wg                                 sync.WaitGroup
	)
	deadline := time.Now().Add(*duration)
	start := time.Now()

	for w := 0; w < *nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(rand.Int63()))
			for time.Now().Before(deadline) {
				from := accounts[rng.Intn(len(accounts))]
				to := accounts[rng.Intn(len(accounts))]
				if from == to {
					continue
				}
				t0 := time.Now()
				status := c.transfer(randKey(), from, to, *amount, "USD")
				lat.record(time.Since(t0))
				switch status {
				case nil:
					atomic.AddInt64(&ok, 1)
				case errConflict:
					atomic.AddInt64(&conflict, 1)
				case errInsufficient:
					atomic.AddInt64(&insufficient, 1)
				default:
					atomic.AddInt64(&failed, 1)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	endTotal := c.sumBalances(append(accounts, treasury))
	total := ok + conflict + insufficient + failed

	fmt.Printf("requests         %d in %s\n", total, elapsed.Round(time.Millisecond))
	fmt.Printf("throughput       %.0f req/s\n", float64(total)/elapsed.Seconds())
	fmt.Printf("  posted         %d (%.1f%%)\n", ok, pct(ok, total))
	fmt.Printf("  insufficient   %d (%.1f%%)   [expected — random pairs drain]\n", insufficient, pct(insufficient, total))
	fmt.Printf("  409 conflict   %d\n", conflict)
	fmt.Printf("  errors         %d\n", failed)
	p := lat.percentiles()
	fmt.Printf("latency          p50 %s   p95 %s   p99 %s   max %s\n",
		p[50].Round(100*time.Microsecond), p[95].Round(100*time.Microsecond),
		p[99].Round(100*time.Microsecond), p[100].Round(100*time.Microsecond))
	fmt.Printf("balance check    start=%d end=%d  %s\n", startTotal, endTotal,
		map[bool]string{true: "CONSERVED ✓", false: "DRIFTED ✗"}[startTotal == endTotal])

	if failed > 0 || startTotal != endTotal {
		os.Exit(1)
	}
}

// --- HTTP client ---

type client struct {
	base string
	http *http.Client
}

var (
	errConflict     = fmt.Errorf("conflict")
	errInsufficient = fmt.Errorf("insufficient")
)

// do issues a request and returns the status code plus the decoded body (into
// dst, if non-nil). It always drains and closes the response body.
func (c *client) do(method, path string, payload, dst any, headers map[string]string) (int, error) {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if dst != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *client) createAccount(ccy string, overdraft bool) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	_, err := c.do(http.MethodPost, "/v1/accounts",
		map[string]any{"name": "lt", "currency": ccy, "allow_overdraft": overdraft}, &out, nil)
	return out.ID, err
}

func (c *client) transfer(key, debit, credit string, amount int64, ccy string) error {
	code, err := c.do(http.MethodPost, "/v1/transfers", map[string]any{
		"debit_account_id": debit, "credit_account_id": credit,
		"amount": amount, "currency": ccy,
	}, nil, map[string]string{"Idempotency-Key": key})
	if err != nil {
		return err
	}
	switch code {
	case http.StatusCreated:
		return nil
	case http.StatusConflict:
		return errConflict
	case http.StatusUnprocessableEntity:
		return errInsufficient
	default:
		return fmt.Errorf("status %d", code)
	}
}

func (c *client) sumBalances(ids []string) int64 {
	var sum int64
	for _, id := range ids {
		var out struct {
			Balance int64 `json:"balance"`
		}
		_, err := c.do(http.MethodGet, "/v1/accounts/"+id, nil, &out, nil)
		must(err)
		sum += out.Balance
	}
	return sum
}

// --- helpers ---

type latencies struct {
	mu sync.Mutex
	d  []time.Duration
}

func (l *latencies) record(d time.Duration) {
	l.mu.Lock()
	l.d = append(l.d, d)
	l.mu.Unlock()
}

func (l *latencies) percentiles() map[int]time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	sort.Slice(l.d, func(i, j int) bool { return l.d[i] < l.d[j] })
	out := map[int]time.Duration{}
	for _, p := range []int{50, 95, 99, 100} {
		if len(l.d) == 0 {
			continue
		}
		idx := (p * len(l.d) / 100) - 1
		if idx < 0 {
			idx = 0
		}
		out[p] = l.d[idx]
	}
	return out
}

func randKey() string { return fmt.Sprintf("lt-%d-%d", time.Now().UnixNano(), rand.Int63()) }
func pct(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) * 100 / float64(d)
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
