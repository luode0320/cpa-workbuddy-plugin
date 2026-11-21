package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// newKeepaliveProbeAccount 在隔离账号目录里落一个真实账号文件并回读。
//
// 必须先落盘再回读：保活落盘走的是 Jar 的 persist，而 Jar 只有在账号带了
// PhysicalPath 时才会 bind，未 bind 的 Jar 写不回任何文件。
// [参数] t: 测试上下文。
// [返回] 带 PhysicalPath 的账号副本。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，保活用例的账号夹具。
func newKeepaliveProbeAccount(t *testing.T) *StoredAuth {
	t.Helper()
	isolateAccounts(t)
	sa := completionTestAccount()
	sa.MsToken = "stale-keepalive-token"
	targetPath := filepath.Join(getAccountsDir(), sa.AuthID+".json")
	if errWrite := writeAuthFileDirect(targetPath, sa); errWrite != nil {
		t.Fatalf("预置账号文件失败：%v", errWrite)
	}
	saved, errRead := readAuthFile(targetPath)
	if errRead != nil {
		t.Fatalf("回读账号文件失败：%v", errRead)
	}
	return saved
}

// startKeepaliveServer 起一个只回响应头的探测端点。
// [参数] t: 测试上下文；token: 要下发的 msToken（空串表示不下发该头）。
// [返回] httptest 服务端。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，保活端点夹具。
func startKeepaliveServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			w.Header().Set(msTokenHeader, token)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"","data":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRefreshMsTokenByProbeSavesNewToken 校验探测到新 token 时写回账号文件。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，保活主路径。
func TestRefreshMsTokenByProbeSavesNewToken(t *testing.T) {
	sa := newKeepaliveProbeAccount(t)
	fresh := "FreshKeepaliveTokenABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	srv := startKeepaliveServer(t, fresh)
	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	refreshMsTokenByProbe(context.Background(), sa)

	if sa.MsToken != fresh {
		t.Fatalf("内存里的 ms_token 应被换成新值，实际 %q", sa.MsToken)
	}
	saved, errRead := readAuthFile(sa.PhysicalPath)
	if errRead != nil {
		t.Fatalf("回读账号文件失败：%v", errRead)
	}
	if saved.MsToken != fresh {
		t.Fatalf("落盘的 ms_token 应是 %q，实际 %q", fresh, saved.MsToken)
	}
}

// TestRefreshMsTokenByProbeSkipsUnchanged 校验同值不重复落盘。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，避免无意义的文件抖动。
func TestRefreshMsTokenByProbeSkipsUnchanged(t *testing.T) {
	sa := newKeepaliveProbeAccount(t)
	sa.MsToken = "SameKeepaliveTokenABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	if errWrite := writeAuthFileDirect(sa.PhysicalPath, sa); errWrite != nil {
		t.Fatalf("预置账号文件失败：%v", errWrite)
	}
	srv := startKeepaliveServer(t, sa.MsToken)
	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	before := fileDigest(sa.PhysicalPath)
	refreshMsTokenByProbe(context.Background(), sa)
	after := fileDigest(sa.PhysicalPath)

	if before != after {
		t.Fatalf("同值不应改写文件：%s -> %s", before, after)
	}
}

// TestRefreshMsTokenByProbeIgnoresMalformed 校验畸形 token 不落盘。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，脏值保护。
func TestRefreshMsTokenByProbeIgnoresMalformed(t *testing.T) {
	sa := newKeepaliveProbeAccount(t)
	original := sa.MsToken
	srv := startKeepaliveServer(t, "bad token with spaces and 中文")
	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	refreshMsTokenByProbe(context.Background(), sa)

	if sa.MsToken != original {
		t.Fatalf("畸形 token 不应写回，实际 %q", sa.MsToken)
	}
	saved, errRead := readAuthFile(sa.PhysicalPath)
	if errRead != nil {
		t.Fatalf("回读账号文件失败：%v", errRead)
	}
	if saved.MsToken != original {
		t.Fatalf("畸形 token 不应落盘，实际 %q", saved.MsToken)
	}
}

// TestRunKeepaliveOnceSkipsDisabled 校验本轮保活跳过停用账号。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，停用账号不应产生上游流量。
func TestRunKeepaliveOnceSkipsDisabled(t *testing.T) {
	sa := newKeepaliveProbeAccount(t)
	sa.Disabled = true
	if errWrite := writeAuthFileDirect(sa.PhysicalPath, sa); errWrite != nil {
		t.Fatalf("预置停用账号失败：%v", errWrite)
	}

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set(msTokenHeader, "ShouldNotBeUsedTokenABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	restore := swapBusinessEndpoint(srv.URL)
	defer restore()

	runKeepaliveOnce(context.Background())

	if hits != 0 {
		t.Fatalf("停用账号不应被保活，实际请求 %d 次", hits)
	}
}

// TestRestartKeepaliveToggles 校验开关能真实启停定时器。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，开关状态迁移。
func TestRestartKeepaliveToggles(t *testing.T) {
	t.Cleanup(stopKeepalive)

	restartKeepalive(Config{KeepaliveEnabled: true, KeepaliveIntervalSeconds: 3600})
	keepaliveMu.Lock()
	running := keepaliveCancel != nil
	keepaliveMu.Unlock()
	if !running {
		t.Fatal("开启开关后应有运行中的定时器")
	}

	restartKeepalive(Config{KeepaliveEnabled: false})
	keepaliveMu.Lock()
	running = keepaliveCancel != nil
	keepaliveMu.Unlock()
	if running {
		t.Fatal("关闭开关后定时器应已停止")
	}
}

// TestStopKeepaliveIsIdempotent 校验空句柄上反复停止不会 panic 或阻塞。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，shutdown 可能重复调用。
func TestStopKeepaliveIsIdempotent(t *testing.T) {
	stopKeepalive()
	stopKeepalive()

	restartKeepalive(Config{KeepaliveEnabled: true, KeepaliveIntervalSeconds: 3600})
	started := time.Now()
	stopKeepalive()
	stopKeepalive()
	if elapsed := time.Since(started); elapsed > keepaliveStopGrace {
		t.Fatalf("停止耗时 %v 超过宽限期 %v", elapsed, keepaliveStopGrace)
	}
}

// TestConfigurePluginKeepaliveDefaults 校验默认开启且间隔回落到默认值。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 22:05:00；改动原因：默认改为开启，断言随之调整。
func TestConfigurePluginKeepaliveDefaults(t *testing.T) {
	t.Cleanup(stopKeepalive)
	if err := configurePlugin(nil); err != nil {
		t.Fatalf("空配置不应报错：%v", err)
	}
	cfg := currentConfig()
	if !cfg.KeepaliveEnabled {
		t.Error("保活默认应为开启")
	}
	if cfg.KeepaliveIntervalSeconds != defaultKeepaliveIntervalSeconds {
		t.Errorf("保活间隔默认应为 %d，实际 %d", defaultKeepaliveIntervalSeconds, cfg.KeepaliveIntervalSeconds)
	}
}

// TestConfigurePluginKeepaliveSurvivesUnrelatedReconfigure 校验无关配置不会把保活冲掉。
//
// 这是默认值从 false 改成 true 之后最容易漏的一条：Go 的 bool 零值是 false，
// 若用值类型解码，只写 timeout_seconds 的一次 reconfigure 就会把保活静默关掉。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 22:05:00；改动原因：新增，布尔默认值的指针解码回归。
func TestConfigurePluginKeepaliveSurvivesUnrelatedReconfigure(t *testing.T) {
	t.Cleanup(stopKeepalive)
	raw := mustJSON(t, map[string]any{
		"config_yaml": []byte("timeout_seconds: 90\n"),
	})
	if err := configurePlugin(raw); err != nil {
		t.Fatalf("解析配置失败：%v", err)
	}
	cfg := currentConfig()
	if cfg.TimeoutSeconds != 90 {
		t.Errorf("timeout_seconds 应为 90，实际 %d", cfg.TimeoutSeconds)
	}
	if !cfg.KeepaliveEnabled {
		t.Error("只改超时不应把保活关掉（缺省键必须保留默认值）")
	}
}

// TestConfigurePluginKeepaliveExplicitOff 校验显式写 false 能真正关掉。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 22:05:00；改动原因：新增，确认指针解码不吞掉显式关闭。
func TestConfigurePluginKeepaliveExplicitOff(t *testing.T) {
	t.Cleanup(stopKeepalive)
	raw := mustJSON(t, map[string]any{
		"config_yaml": []byte("keepalive_enabled: false\n"),
	})
	if err := configurePlugin(raw); err != nil {
		t.Fatalf("解析配置失败：%v", err)
	}
	if currentConfig().KeepaliveEnabled {
		t.Error("配置里显式写了 false，运行期应为关闭")
	}
	keepaliveMu.Lock()
	running := keepaliveCancel != nil
	keepaliveMu.Unlock()
	if running {
		t.Error("关闭后不应留下运行中的定时器")
	}
}

// TestConfigurePluginKeepaliveFromYAML 校验 YAML 能开启保活并覆盖间隔。
// [参数] t: 测试上下文。
// [返回] 无。
// 最近修改时间：2026-11-21 21:50:00；改动原因：新增，配置解析路径。
func TestConfigurePluginKeepaliveFromYAML(t *testing.T) {
	t.Cleanup(stopKeepalive)
	raw := mustJSON(t, map[string]any{
		"config_yaml": []byte("keepalive_enabled: true\nkeepalive_interval_seconds: 900\n"),
	})
	if err := configurePlugin(raw); err != nil {
		t.Fatalf("解析保活配置失败：%v", err)
	}
	cfg := currentConfig()
	if !cfg.KeepaliveEnabled {
		t.Error("配置里写了 keepalive_enabled: true，运行期应为开启")
	}
	if cfg.KeepaliveIntervalSeconds != 900 {
		t.Errorf("保活间隔应为 900，实际 %d", cfg.KeepaliveIntervalSeconds)
	}
	// 其余默认项不能被顺带冲成 0。
	if cfg.TimeoutSeconds != 120 || cfg.StreamTimeoutSeconds != 600 {
		t.Errorf("对话超时应保持默认 120/600，实际 %d/%d", cfg.TimeoutSeconds, cfg.StreamTimeoutSeconds)
	}
}
