// checkin_headers_test.go guards the check-in request shaping: browser-like
// headers (WAF penalty avoidance) and sane device-id composition (no
// leading-dash ids when the credential carries no device fingerprint).
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestDeviceIDFor 验证签到设备标识派生恒为合规 16 位纯数字且具备同设备多账号去重隔离，防止出现连字符拼接或尾零畸形触发 9074 风控，无外部副作用。
// 最近修改时间：2026-10-10 22:00:00；改动原因：对齐合规 16 位纯数字设备标识契约与存量尾零 ID 归一化测试。
func TestDeviceIDFor(t *testing.T) {
	// 1. 空输入应返回空字符串
	if got := deviceIDFor("", ""); got != "" {
		t.Fatalf("deviceIDFor empty = %q, want empty", got)
	}
	// 2. 单一合规 16 位客户端设备号或用户号应原样复用
	if got := deviceIDFor("3418807932843306", ""); got != "3418807932843306" {
		t.Errorf("valid base only = %q, want 3418807932843306", got)
	}
	if got := deviceIDFor("", "2033439621254311"); got != "2033439621254311" {
		t.Errorf("valid uid only = %q, want 2033439621254311", got)
	}
	// 3. 存量尾零畸形设备号与多账号组合应确定性派生为合规 16 位纯数字且互不冲突
	targetGot := deviceIDFor("9670064000000000", "1114256688551036")
	if !isValidCheckinDeviceID(targetGot) || strings.Contains(targetGot, "-") {
		t.Fatalf("target account deviceIDFor = %q, want valid 16-digit id", targetGot)
	}
	if again := deviceIDFor("9670064000000000", "1114256688551036"); again != targetGot {
		t.Errorf("deviceIDFor not deterministic: %q vs %q", targetGot, again)
	}
	otherGot := deviceIDFor("9670064000000000", "2433670276462265")
	if !isValidCheckinDeviceID(otherGot) || otherGot == targetGot {
		t.Errorf("same base different uid must yield distinct valid 16-digit ids: %q vs %q", targetGot, otherGot)
	}
}

func TestCheckinAuthHeaders_ClientLike(t *testing.T) {
	a := &traeAuth{Token: "tok", DeviceID: "dev", UserID: "u1"}
	h := checkinAuthHeaders(a, "dev-u1")
	if got := h.Get("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") {
		t.Errorf("User-Agent = %q, want client UA", got)
	}
	if got := h.Get("Origin"); got != "" {
		t.Errorf("Origin = %q, want empty (client request must NOT contain web origin)", got)
	}
	if got := h.Get("Referer"); got != "" {
		t.Errorf("Referer = %q, want empty (client request must NOT contain web referer)", got)
	}
	if got := h.Get("Authorization"); got != "Cloud-IDE-JWT tok" {
		t.Errorf("Authorization = %q, unchanged expected", got)
	}
	if got := h.Get("x-device-id"); got != "dev-u1" {
		t.Errorf("x-device-id = %q", got)
	}
	if got := h.Get("x-device-brand"); got == "" {
		t.Error("x-device-brand missing")
	}
	if got := h.Get("x-device-type"); got == "" {
		t.Error("x-device-type missing")
	}
	if got := h.Get("x-os-version"); got == "" {
		t.Error("x-os-version missing")
	}
	if got := h.Get("x-app-version"); got == "" {
		t.Error("x-app-version missing")
	}
	if got := h.Get("x-app-id"); got == "" {
		t.Error("x-app-id missing")
	}
	if h.Get("Content-Type") != "application/json" {
		t.Error("Content-Type missing")
	}
}

func TestCheckinClaimRequest_JSONPayload(t *testing.T) {
	payload, err := json.Marshal(checkinClaimRequest{ReqSource: 2})
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if string(payload) != `{"req_source":2}` {
		t.Errorf("got payload %s, want {\"req_source\":2}", string(payload))
	}
}

func TestCheckinAuthHeaders_NoBackticks(t *testing.T) {
	// Guard against raw-string-breaking characters sneaking into header values.
	a := &traeAuth{Token: "tok"}
	h := checkinAuthHeaders(a, "d")
	for k, vs := range h {
		for _, v := range vs {
			if strings.ContainsAny(v, "`") {
				t.Errorf("header %s contains backtick: %q", k, v)
			}
		}
	}
	_ = http.Header(h)
}
