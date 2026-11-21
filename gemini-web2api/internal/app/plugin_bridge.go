package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/accounts"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/adapter"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/auth"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/balancer"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/gemini"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/config"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/logging"
	"github.com/luode0320/cpa-workbuddy-plugin/gemini-web2api/internal/requestdb"
)

const dataDir = "data"

// defaultVersion 是编译期未注入版本号时的兜底值，与 VERSION 文件保持一致。
const defaultVersion = "1.0.14"

// version 保存当前插件版本号，由 main 包在启动时用 SetVersion 注入，
// 使 CI 的 -X main.version 与注册上报的版本号保持单一来源。
var version = defaultVersion

// SetVersion 注入当前插件版本号；传入空值时保留原值。
func SetVersion(v string) {
	if s := strings.TrimSpace(v); s != "" {
		version = s
	}
}

// Version returns the plugin runtime version.
func Version() string {
	return version
}

func authDir(paths []string) string {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return path
		}
	}
	return "auth"
}

// WithPinnedAccountID attaches a specific Gemini account ID to the request context.
func WithPinnedAccountID(ctx context.Context, accountID string) context.Context {
	return balancer.WithPinnedAccountID(ctx, accountID)
}

// ContextWithPinnedAccountID is an alias for WithPinnedAccountID.
func ContextWithPinnedAccountID(ctx context.Context, accountID string) context.Context {
	return balancer.WithPinnedAccountID(ctx, accountID)
}

// PinnedAccountIDFromContext returns the pinned account ID from context when set.
func PinnedAccountIDFromContext(ctx context.Context) string {
	return balancer.PinnedAccountIDFromContext(ctx)
}

// PluginRuntime wraps the Gemini-Web2API runtime components for in-process CLIProxyAPI plugin hosting.
type PluginRuntime struct {
	ctx        context.Context
	cancel     context.CancelFunc
	DataDir    string
	AuthDir    string
	Cfg        config.Config
	Pool       *balancer.AccountPool
	Accounts   *accounts.Manager
	Store      *requestdb.Store
	Handler    http.Handler
	StartedAt  time.Time
	stopping   chan struct{}
	stopOnce   sync.Once
	running    atomic.Bool
	startMu    sync.Mutex
	started    bool
}

// NewPluginRuntime initializes an in-process Gemini-Web2API runtime without opening a network listener.
func NewPluginRuntime(ctx context.Context, cfg config.Config, runtimeDataDir string, autoStart bool, guestEnabled bool) (*PluginRuntime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runtimeCtx, cancel := context.WithCancel(ctx)

	if strings.TrimSpace(runtimeDataDir) == "" {
		runtimeDataDir = dataDir
	}
	if err := os.MkdirAll(runtimeDataDir, 0o700); err != nil {
		cancel()
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	_ = logging.Setup(logging.Options{
		Level:  cfg.LogLevel,
		Format: cfg.LogFormat,
		Writer: io.Discard,
	})
	if cfg.LogFile {
		logPath := filepath.Join(runtimeDataDir, "gemini-web2api.log")
		_ = logging.SetFile(logPath, cfg.LogFile, cfg.LogMaxTotalMB)
	}
	go logging.Watch(runtimeCtx)

	store, err := requestdb.Open(filepath.Join(runtimeDataDir, "requests.db"))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open request db: %w", err)
	}
	store.SetBodyCapture(cfg.RequestBodyLog)
	if err := gemini.LoadModelNames(store); err != nil {
		_ = store.Close()
		cancel()
		return nil, fmt.Errorf("load model names: %w", err)
	}
	go recordLogs(runtimeCtx, store)

	authDirectory := authDir(cfg.AuthStatePaths)
	if len(cfg.AuthStatePaths) > 0 && strings.TrimSpace(cfg.AuthStatePaths[0]) != "" {
		first := strings.TrimSpace(cfg.AuthStatePaths[0])
		if !strings.HasSuffix(strings.ToLower(first), ".json") {
			authDirectory = first
		}
	}
	if err := os.MkdirAll(authDirectory, 0o700); err != nil {
		_ = store.Close()
		cancel()
		return nil, fmt.Errorf("create auth dir: %w", err)
	}

	config.SetModelMapping(cfg.ModelMapping)
	adapter.ConfigureSessionTTL(cfg.SessionTTL)
	pool := balancer.NewAccountPool(cfg.AccountCooldown, cfg.SessionTTL)
	if stored, err := store.Cooldowns(runtimeCtx, time.Now()); err == nil && len(stored) > 0 {
		restored := make([]balancer.Cooldown, 0, len(stored))
		for _, sc := range stored {
			restored = append(restored, balancer.Cooldown{
				Account: sc.Account,
				Model:   sc.Model,
				Until:   sc.Until,
				Reason:  sc.Reason,
			})
		}
		pool.RestoreCooldowns(restored)
	}
	pool.OnCooldownChange(func(cooldowns []balancer.Cooldown) {
		stored := make([]requestdb.StoredCooldown, 0, len(cooldowns))
		for _, c := range cooldowns {
			stored = append(stored, requestdb.StoredCooldown{
				Account: c.Account,
				Model:   c.Model,
				Until:   c.Until,
				Reason:  c.Reason,
			})
		}
		if err := store.SaveCooldowns(runtimeCtx, stored); err != nil {
			logging.Warn(logging.SourceService, "保存账号冷却失败", "错误", err)
		}
	})
	pool.SetStrategy(cfg.RoutingStrategy)
	pool.SetAccountOrder(cfg.AccountOrder)
	pool.SetModelAccounts(cfg.ModelAccounts)
	adapter.SetFirstEventHedge(parseFirstEventHedge(cfg))
	adapter.SetResponseModes(cfg.NonstreamKeepalive, cfg.FakeStream)
	adapter.SetContinueSession(cfg.ContinueSession)
	manager := accounts.New(runtimeCtx, pool, accounts.Settings{
		InitTimeout:   cfg.InitTimeout,
		RetryInterval: cfg.AccountCooldown,
		Proxy:         cfg.ProxyURL,
		SaveHistory:   cfg.SaveHistory,
		AuthDir:       authDirectory,
		Concurrency:   cfg.PerAccountConcurrency,
	})

	stopping := make(chan struct{})
	startedAt := time.Now()
	router := NewRouter(Deps{
		Pool:     pool,
		Store:    store,
		Accounts: manager,
	})

	rt := &PluginRuntime{
		ctx:       runtimeCtx,
		cancel:    cancel,
		DataDir:   runtimeDataDir,
		AuthDir:   authDirectory,
		Cfg:       cfg,
		Pool:      pool,
		Accounts:  manager,
		Store:     store,
		Handler:   router,
		StartedAt: startedAt,
		stopping:  stopping,
	}
	go adapter.CleanupTemporaryChats(runtimeCtx, pool)

	if autoStart {
		go rt.StartBackground(guestEnabled)
	}

	return rt, nil
}

// StartBackground loads accounts from AuthStatePaths and starts the account manager in the background.
func (rt *PluginRuntime) StartBackground(guestEnabled bool) {
	if rt == nil {
		return
	}
	rt.startMu.Lock()
	if rt.started {
		rt.startMu.Unlock()
		return
	}
	rt.started = true
	rt.running.Store(true)
	authPaths := append([]string(nil), rt.Cfg.AuthStatePaths...)
	rt.startMu.Unlock()

	loaded, err := auth.LoadFiles(authPaths)
	if err != nil {
		logging.Warn(logging.SourceService, "加载认证目录失败", "错误", err)
	}
	ids := make([]string, 0, len(loaded))
	for _, acc := range loaded {
		ids = append(ids, acc.ID)
	}
	logging.SetSourceWidth(ids)

	rt.Accounts.Start(loaded)
	if !guestEnabled {
		rt.Accounts.StopGuest()
	}
}

// ApplyConfig updates runtime settings in place when the plugin is reloaded.
func (rt *PluginRuntime) ApplyConfig(next config.Config, guestEnabled bool) {
	if rt == nil {
		return
	}
	rt.startMu.Lock()
	rt.Cfg = next
	rt.startMu.Unlock()
	config.SetModelMapping(next.ModelMapping)
	rt.Pool.SetTimings(next.AccountCooldown, next.SessionTTL)
	adapter.ConfigureSessionTTL(next.SessionTTL)
	if level, err := logging.ParseLevel(next.LogLevel); err == nil {
		logging.SetLevel(level)
	}
	rt.Store.SetBodyCapture(next.RequestBodyLog)
	rt.Pool.SetStrategy(next.RoutingStrategy)
	rt.Pool.SetAccountOrder(next.AccountOrder)
	rt.Pool.SetModelAccounts(next.ModelAccounts)
	adapter.SetFirstEventHedge(parseFirstEventHedge(next))
	adapter.SetResponseModes(next.NonstreamKeepalive, next.FakeStream)
	adapter.SetContinueSession(next.ContinueSession)
	rt.Accounts.SetSettings(accounts.Settings{
		InitTimeout:   next.InitTimeout,
		RetryInterval: next.AccountCooldown,
		Proxy:         next.ProxyURL,
		SaveHistory:   next.SaveHistory,
		AuthDir:       rt.AuthDir,
		Concurrency:   next.PerAccountConcurrency,
	})
	if !guestEnabled {
		rt.Accounts.StopGuest()
	} else if st := rt.Pool.Status(); st.Guest == nil && rt.running.Load() {
		rt.Accounts.RelaunchGuest()
	}
}

func parseFirstEventHedge(cfg config.Config) adapter.FirstEventHedge {
	mode := strings.TrimSpace(cfg.FirstEventHedge)
	if strings.EqualFold(mode, "off") {
		return adapter.FirstEventHedge{Auto: false, After: 0, Models: cfg.FirstEventHedgeModels}
	}
	if mode == "" || strings.EqualFold(mode, "auto") {
		return adapter.FirstEventHedge{Auto: true, Models: cfg.FirstEventHedgeModels}
	}
	if seconds, err := strconv.Atoi(mode); err == nil && seconds > 0 {
		return adapter.FirstEventHedge{After: time.Duration(seconds) * time.Second, Models: cfg.FirstEventHedgeModels}
	}
	if d, err := time.ParseDuration(mode); err == nil && d > 0 {
		return adapter.FirstEventHedge{After: d, Models: cfg.FirstEventHedgeModels}
	}
	return adapter.FirstEventHedge{Auto: true, Models: cfg.FirstEventHedgeModels}
}

// Close stops all account workers, deletes temporary web chats, closes SSE streams, and closes the SQLite request store.
func (rt *PluginRuntime) Close() error {
	if rt == nil {
		return nil
	}
	rt.running.Store(false)
	rt.stopOnce.Do(func() {
		close(rt.stopping)
	})
	if rt.Pool != nil {
		cleanupCtx, stopCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		adapter.DeleteTemporaryChats(cleanupCtx, rt.Pool)
		stopCleanup()
	}
	if rt.cancel != nil {
		rt.cancel()
	}
	if rt.Store != nil {
		adapter.DetachStorage(rt.Store)
		gemini.DetachModelNameStore(rt.Store)
		return rt.Store.Close()
	}
	return nil
}

// ReadAccountStorageState reads the raw storage-state.json bytes for an account ID.
func (rt *PluginRuntime) ReadAccountStorageState(accountID string) ([]byte, *auth.StorageState, error) {
	if rt == nil {
		return nil, nil, fmt.Errorf("runtime not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil, fmt.Errorf("account_id is empty")
	}
	path := filepath.Join(rt.AuthDir, accountID, "storage-state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var state auth.StorageState
	if err := json.Unmarshal(raw, &state); err != nil {
		return raw, nil, err
	}
	return raw, &state, nil
}

func recordLogs(ctx context.Context, store *requestdb.Store) {
	for {
		_, events, cancel := logging.Subscribe(0)
		for open := true; open; {
			select {
			case <-ctx.Done():
				cancel()
				return
			case event, ok := <-events:
				open = ok
				if ok && (event.Level == "WARN" || event.Level == "ERROR") {
					store.RecordLog(event)
				}
			}
		}
	}
}
