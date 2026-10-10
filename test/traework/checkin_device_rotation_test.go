package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCheckinAccount_RotatesDeviceIDOn9074AndDeviceBlocked 验证签到遇到 9074 限流或 9095 设备去重时自动切换新 16 位设备号重试成功，防止死锁在同一被拦截设备号，无外部网络副作用。
// 最近修改时间：2026-10-10 22:00:00；改动原因：新增 9074 与 9095 设备号自动轮换重试单测。
func TestCheckinAccount_RotatesDeviceIDOn9074AndDeviceBlocked(t *testing.T) {
	// 1. 启动本地模拟签到服务端，前两次分别返回 9074 与 9095，第三次返回成功
	var seenDevs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenDevs = append(seenDevs, r.Header.Get("x-device-id"))
		w.Header().Set("Content-Type", "application/json")
		switch len(seenDevs) {
		case 1:
			_, _ = w.Write([]byte(`{"code":9074,"message":"当前参与用户太多，请稍后再试"}`))
		case 2:
			_, _ = w.Write([]byte(`{"code":9095,"message":"当前设备今日已经签到，请明日再来哦～"}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"message":"success","points":100}`))
		}
	}))
	defer srv.Close()

	// 2. 使用生产「用户04878311608」同款尾零 deviceId 执行签到并验证三次设备号均合规且互不相同
	acc := &traeAuth{Token: "tok", Host: srv.URL, DeviceID: "9670064000000000", UserID: "1114256688551036"}
	res := checkinAccount(acc)
	if !res.OK || res.Points != 100 {
		t.Fatalf("checkinAccount res = %+v, want OK with 100 points", res)
	}
	if len(seenDevs) != 3 {
		t.Fatalf("attempts = %d, want 3", len(seenDevs))
	}
	for i, d := range seenDevs {
		if !isValidCheckinDeviceID(d) {
			t.Errorf("attempt %d device id %q is not valid 16-digit client id", i, d)
		}
	}
	if seenDevs[0] == seenDevs[1] || seenDevs[1] == seenDevs[2] {
		t.Errorf("expected rotated device ids across retries, got %v", seenDevs)
	}
}
