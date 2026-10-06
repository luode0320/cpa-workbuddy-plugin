package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

var billingRetryDelays = []time.Duration{time.Second, 2 * time.Second}

type resourcePackage struct {
	PackageName         string `json:"PackageName"`
	CapacityRemain      int64  `json:"CapacityRemain"`
	CapacityUsed        int64  `json:"CapacityUsed"`
	CapacitySize        int64  `json:"CapacitySize"`
	CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
	CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
	CycleCapacitySize   int64  `json:"CycleCapacitySize"`
	CycleStartTime      string `json:"CycleStartTime"`
	CycleEndTime        string `json:"CycleEndTime"`
}

type packageSummary struct {
	Name       string `json:"name"`
	Remain     int64  `json:"remain"`
	Used       int64  `json:"used"`
	Size       int64  `json:"size"`
	CycleStart string `json:"cycle_start,omitempty"`
	CycleEnd   string `json:"cycle_end,omitempty"`
}

type creditsSummary struct {
	TotalRemain int64            `json:"total_remain"`
	TotalUsed   int64            `json:"total_used"`
	TotalSize   int64            `json:"total_size"`
	PackCount   int              `json:"pack_count"`
	FetchedAt   string           `json:"fetched_at,omitempty"`
	Packages    []packageSummary `json:"packages,omitempty"`
}

func billingHeaders(req *http.Request, sa *storedAuth) {
	req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if sa.Account.UID != "" {
		req.Header.Set("X-User-Id", sa.Account.UID)
	}
	if sa.Account.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", sa.Account.EnterpriseID)
		req.Header.Set("X-Tenant-Id", sa.Account.EnterpriseID)
	}
	req.Header.Set("X-Domain", "www.workbuddy.ai")
}

func billingCall(sa *storedAuth, path string, body any) (json.RawMessage, error) {
	data, err := billingCallOnce(sa, path, body)
	for _, d := range billingRetryDelays {
		if err == nil || !isTransientBillingErr(err) {
			break
		}
		time.Sleep(d)
		data, err = billingCallOnce(sa, path, body)
	}
	if err != nil {
		log.Printf("[workbuddy-ai] billing %s failed for %s: %v", path, sa.Account.UID, err)
	}
	return data, err
}

func isTransientBillingErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "http 5") || strings.HasPrefix(msg, "http=5") || strings.Contains(msg, "status 5")
}

func billingCallOnce(sa *storedAuth, path string, body any) (json.RawMessage, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequest(http.MethodPost, upstreamBase+path, reader)
	if err != nil {
		return nil, err
	}
	billingHeaders(req, sa)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	raw := resp.Body
	if resp.StatusCode >= 500 {
		snippet := strings.TrimSpace(redactSecrets(string(raw)))
		if len(snippet) > 120 {
			snippet = snippet[:120]
		}
		return nil, fmt.Errorf("http %d from %s: %s", resp.StatusCode, path, snippet)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		snippet := strings.TrimSpace(redactSecrets(string(raw)))
		if len(snippet) > 120 {
			snippet = snippet[:120]
		}
		return nil, fmt.Errorf("parse failed: %w (body: %s)", err, snippet)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("code=%d msg=%s", env.Code, truncateRedacted(env.Msg, 120))
	}
	return env.Data, nil
}

func packageRemainUsed(a resourcePackage) (remain, used, size int64) {
	if a.CycleCapacitySize > 0 {
		remain = a.CycleCapacityRemain
		size = a.CycleCapacitySize
		if remain < 0 {
			remain = 0
		}
		if remain > size {
			remain = size
		}
		used = size - remain
		if a.CycleCapacityUsed > used {
			used = a.CycleCapacityUsed
			if size >= used {
				remain = size - used
			}
		}
		return remain, used, size
	}
	if a.CycleCapacityRemain > 0 || a.CycleCapacityUsed > 0 {
		remain = a.CycleCapacityRemain
		used = a.CycleCapacityUsed
		if remain < 0 {
			remain = 0
		}
		if used < 0 {
			used = 0
		}
		size = remain + used
		if a.CapacitySize > size {
			size = a.CapacitySize
			if size >= remain {
				used = size - remain
			}
		}
		return remain, used, size
	}
	remain = a.CapacityRemain
	used = a.CapacityUsed
	size = a.CapacitySize
	if remain < 0 {
		remain = 0
	}
	if used < 0 {
		used = 0
	}
	if size <= 0 {
		size = remain + used
	}
	if used == 0 && size > remain {
		used = size - remain
	}
	return remain, used, size
}

func fetchUserResource(sa *storedAuth) (*creditsSummary, error) {
	now := time.Now()
	const pageSize = 100
	body := map[string]any{
		"PageNumber":               1,
		"PageSize":                 pageSize,
		"ProductCode":              "p_tcaca",
		"Status":                   []int{0, 3},
		"PackageEndTimeRangeBegin": now.Format("2006-01-02 15:04:05"),
		"PackageEndTimeRangeEnd":   now.Add(365 * 101 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	}
	data, err := billingCall(sa, "/v2/billing/meter/get-user-resource", body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Response struct {
			Data struct {
				TotalCount  int64             `json:"TotalCount"`
				TotalDosage int64             `json:"TotalDosage"`
				Accounts    []resourcePackage `json:"Accounts"`
			} `json:"Data"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	sum := &creditsSummary{}
	for _, a := range resp.Response.Data.Accounts {
		remain, used, size := packageRemainUsed(a)
		sum.TotalRemain += remain
		sum.TotalUsed += used
		sum.TotalSize += size
		sum.Packages = append(sum.Packages, packageSummary{
			Name:       a.PackageName,
			Remain:     remain,
			Used:       used,
			Size:       size,
			CycleStart: a.CycleStartTime,
			CycleEnd:   a.CycleEndTime,
		})
	}
	sum.PackCount = len(sum.Packages)
	if sum.TotalSize > 0 {
		derived := sum.TotalSize - sum.TotalRemain
		if derived < 0 {
			derived = 0
		}
		if derived > sum.TotalUsed {
			sum.TotalUsed = derived
		}
	}
	if dosage := resp.Response.Data.TotalDosage; dosage > sum.TotalSize {
		sum.TotalSize = dosage
		derived := sum.TotalSize - sum.TotalRemain
		if derived < 0 {
			derived = 0
		}
		if derived > sum.TotalUsed {
			sum.TotalUsed = derived
		}
	}
	return sum, nil
}

func fetchPaymentType(sa *storedAuth) string {
	data, err := billingCall(sa, "/v2/billing/meter/get-payment-type", nil)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if s, ok := m["paymentType"].(string); ok {
		return s
	}
	return ""
}

func performTrialCall(sa *storedAuth) (map[string]any, error) {
	data, err := billingCall(sa, "/billing/ide/trial", nil)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "14051") {
			return map[string]any{
				"success":         false,
				"message":         "已领取过专家加油包",
				"already_claimed": true,
			}, nil
		}
		return map[string]any{"success": false, "message": msg}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["success"] = true
	return m, nil
}

func hasTrialPack(cr *creditsSummary) bool {
	if cr == nil {
		return false
	}
	for _, p := range cr.Packages {
		name := strings.ToLower(strings.TrimSpace(p.Name))
		if name == "" {
			continue
		}
		if strings.Contains(name, "trial") {
			return true
		}
		if strings.Contains(name, "pro plan") && (strings.Contains(name, "free") || strings.Contains(name, "one-time") || strings.Contains(name, "2-week") || strings.Contains(name, "2 week")) {
			return true
		}
		if strings.Contains(name, "专家加油") || strings.Contains(name, "专家体验包") {
			return true
		}
	}
	return false
}

func isCreditsExhausted(cr *creditsSummary) bool {
	if cr == nil {
		return false
	}
	if cr.TotalRemain > 0 {
		return false
	}
	if cr.TotalUsed > 0 || cr.TotalSize > 0 {
		return true
	}
	return false
}
