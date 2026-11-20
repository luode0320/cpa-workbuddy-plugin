package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// 本文件实现「会话 → 账号」粘性绑定。
//
// 为什么需要：视频是两段式的，首轮建会话、次轮带续接点追问。选号本身是轮转的
// （见 scheduler.go 的 schedulerIndex），池里有两个以上账号时首轮落 A、确认轮必落
// B。上游只认建立会话的那个账号，跨账号续接拿不到上下文 —— 既出不了片，又把这次
// 追问的额度白烧在健康账号上。粘性表把首轮实际使用的账号记下来，续接请求照表回钉。
//
// 与 failover.go 的分工：粘性表只回答「该用谁」，冷却与健康度判定仍归 failover。

// sessionStickyTTL 是粘性绑定的有效期。
//
// 取 30 分钟：真实两段式的人机间隔实测在 1~3 分钟，半小时足够覆盖正常确认；上游
// 会话自身也会过期，再长只会让表在内存里堆无效条目。
var sessionStickyTTL = 30 * time.Minute

// sessionStickyEntry 是一条「会话 → 账号」绑定。
type sessionStickyEntry struct {
	// authID 是首轮实际执行成功的账号标识。
	authID string
	// expiresAt 是绑定失效时刻。
	expiresAt time.Time
}

var (
	// sessionStickyMu 保护 sessionStickyTable。
	sessionStickyMu sync.Mutex
	// sessionStickyTable 是 conversation_id → 绑定。
	sessionStickyTable = map[string]sessionStickyEntry{}
)

// rememberSessionAuth 记录会话与账号的绑定。
//
// 只在首轮真正成功后被调用：失败的账号不能绑定，否则续接会被钉在一个已经挂掉的
// 账号上，用户重试也没救。
// [参数] conversationID: 上游分配的会话 id；authID: 本次实际执行成功的账号标识。
// [返回] 无。
// 最近修改时间：2026-11-21 00:15:00；改动原因：新增，多账号会话粘性。
func rememberSessionAuth(conversationID, authID string) {
	conversationID = strings.TrimSpace(conversationID)
	authID = strings.TrimSpace(authID)
	if conversationID == "" || authID == "" {
		return
	}

	sessionStickyMu.Lock()
	defer sessionStickyMu.Unlock()

	// 顺手清过期条目：这张表只在请求路径上写，没有独立清理协程，靠写时顺带扫一遍。
	now := time.Now()
	for key, entry := range sessionStickyTable {
		if now.After(entry.expiresAt) {
			delete(sessionStickyTable, key)
		}
	}
	sessionStickyTable[conversationID] = sessionStickyEntry{authID: authID, expiresAt: now.Add(sessionStickyTTL)}
}

// lookupSessionAuth 查会话绑定的账号。
// [参数] conversationID: 会话 id。
// [返回] 绑定的账号标识；没有绑定或已过期时返回空串。
// 最近修改时间：2026-11-21 00:15:00；改动原因：新增，多账号会话粘性。
func lookupSessionAuth(conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ""
	}

	sessionStickyMu.Lock()
	defer sessionStickyMu.Unlock()

	entry, ok := sessionStickyTable[conversationID]
	if !ok {
		return ""
	}
	if time.Now().After(entry.expiresAt) {
		delete(sessionStickyTable, conversationID)
		return ""
	}
	return entry.authID
}

// resetSessionSticky 清空粘性表。
//
// 只给用例用：表是包级状态，用例之间必须互不污染，否则「上一次的绑定」会让本次
// 的选号断言莫名其妙地通过或失败。
// [参数] 无。
// [返回] 无。
// 最近修改时间：2026-11-21 00:15:00；改动原因：新增，用例隔离。
func resetSessionSticky() {
	sessionStickyMu.Lock()
	defer sessionStickyMu.Unlock()
	sessionStickyTable = map[string]sessionStickyEntry{}
}

// applySessionSticky 按粘性表修正本次请求应使用的账号。
//
// 粘性是硬约束而不是建议：续接请求一旦打到别的账号，上游认不出上下文，结果一定
// 是白等一次超时。因此绑定命中时必须换过去，取不到就报错 —— 不能「凑合用当前
// 账号」，那只会把错误记到一个健康账号头上（failover 会因此冷却它）。
// [参数] sa: 宿主或调度器给出的账号，允许为 nil；resume: 本次请求的续接点。
// [返回] 最终账号与错误。
// 最近修改时间：2026-11-21 00:15:00；改动原因：新增，续接请求回钉首轮账号。
func applySessionSticky(sa *StoredAuth, resume resumePoint) (*StoredAuth, error) {
	// 1. 新会话没有可粘的对象，原样返回。
	if resume.isZero() {
		return sa, nil
	}

	// 2. 表里没有绑定（首次启动、已过期、或是别的实例建的会话）：沿用现有账号。
	boundAuthID := lookupSessionAuth(resume.ConversationID)
	if boundAuthID == "" {
		return sa, nil
	}

	// 3. 已经是绑定的那个账号，直接复用宿主给的凭据快照（它比本地文件新）。
	if sa != nil && sa.AuthID == boundAuthID {
		return sa, nil
	}

	// 4. 换到绑定的账号：从本地池取插件自己维护的那份凭据。
	accounts, errList := listAllAuthFiles()
	if errList != nil {
		return nil, fmt.Errorf("读取账号失败：%w", errList)
	}
	for _, account := range accounts {
		if account.Disabled || account.AuthID != boundAuthID {
			continue
		}
		return account, nil
	}

	return nil, fmt.Errorf("会话 %s 绑定的账号 %s 已不存在或已停用：请重新发起首轮，不要续接旧会话",
		resume.ConversationID, boundAuthID)
}
