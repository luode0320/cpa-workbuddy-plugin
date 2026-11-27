// instance_guard_test.go 覆盖孤儿实例标记识别与守卫闸门。
package main

import (
	"os"
	"strconv"
	"sync"
	"testing"
)

// resetInstanceGuard 清掉进程内登记状态，使 claimPluginInstance 可在测试中重跑。
func resetInstanceGuard(t *testing.T) {
	t.Helper()
	instanceGuardOnce = sync.Once{}
	instanceGuardID = ""
	t.Cleanup(func() {
		instanceGuardOnce = sync.Once{}
		instanceGuardID = ""
		os.Remove(pluginInstancePath())
	})
}

// deadPID 返回一个当前必然不存在的 pid。
func deadPID() int {
	// 取一个远超系统 pid 上限的值，且不从 1 开始以免撞上 init。
	return 1 << 22
}

// TestOrphanOwnerMarker 验证孤儿判定刻意保守：只认能解出 pid 且进程已消失的标记。
func TestOrphanOwnerMarker(t *testing.T) {
	cases := []struct {
		name  string
		owner string
		want  bool
	}{
		{"empty marker is orphan", "", true},
		{"blank marker is orphan", "   ", true},
		{"unparsable marker keeps authority", "newer-instance", false},
		{"trailing dash keeps authority", "abc-", false},
		{"zero pid keeps authority", "abc-0", false},
		{"own pid is not orphan", "abc-" + strconv.Itoa(os.Getpid()), false},
		{"dead pid is orphan", "abc-" + strconv.Itoa(deadPID()), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := orphanOwnerMarker(c.owner); got != c.want {
				t.Fatalf("orphanOwnerMarker(%q) = %v, want %v", c.owner, got, c.want)
			}
		})
	}
}

// TestPluginInstanceSuperseded_TakesOverOrphanMarker 验证死进程留下的标记会被
// 接管，而不是永久压死本实例的常驻循环。
func TestPluginInstanceSuperseded_TakesOverOrphanMarker(t *testing.T) {
	resetInstanceGuard(t)
	orphan := "orphan-" + strconv.Itoa(deadPID())
	if err := os.WriteFile(pluginInstancePath(), []byte(orphan), 0o644); err != nil {
		t.Fatalf("seed orphan marker: %v", err)
	}

	if pluginInstanceSuperseded() {
		t.Fatal("orphan marker must not supersede a live instance")
	}
	raw, err := os.ReadFile(pluginInstancePath())
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got := string(raw); got != instanceGuardID {
		t.Fatalf("marker = %q, want this instance id %q", got, instanceGuardID)
	}
}

// TestPluginInstanceSuperseded_DetectsNewerInstance 验证另一实例写入的标记会让
// 本实例判定为已被取代。用当前进程 pid 冒充存活实例，保证跨平台确定性。
func TestPluginInstanceSuperseded_DetectsNewerInstance(t *testing.T) {
	resetInstanceGuard(t)
	claimPluginInstance()
	newer := "newer-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(pluginInstancePath(), []byte(newer), 0o644); err != nil {
		t.Fatalf("seed newer marker: %v", err)
	}
	if !pluginInstanceSuperseded() {
		t.Fatal("a marker owned by another live instance must supersede this one")
	}
}

// TestRetireIfSuperseded 验证守卫闸门在标记归属其他实例时返回 true。
func TestRetireIfSuperseded(t *testing.T) {
	resetInstanceGuard(t)
	claimPluginInstance()
	if err := os.WriteFile(pluginInstancePath(), []byte("other-instance"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	if !retireIfSuperseded("token-keepalive") {
		t.Fatal("retireIfSuperseded must report true for a superseded instance")
	}
}
