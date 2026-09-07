package main

import (
	"testing"
	"time"
)

func TestJsonBool(t *testing.T) {
	m := map[string]any{"a": true, "b": float64(1), "c": "true", "d": "false", "e": "1", "f": float64(0)}
	if !jsonBool(m, "a") {
		t.Error("a")
	}
	if !jsonBool(m, "b") {
		t.Error("b")
	}
	if !jsonBool(m, "c") {
		t.Error("c")
	}
	if !jsonBool(m, "e") {
		t.Error("e")
	}
	if jsonBool(m, "d") {
		t.Error("d")
	}
	if jsonBool(m, "f") {
		t.Error("f")
	}
	if jsonBool(m, "missing") {
		t.Error("missing")
	}
	if !jsonBool(m, "missing", "a") {
		t.Error("fallback a")
	}
}
func TestJsonI64(t *testing.T) {
	m := map[string]any{"a": float64(42), "b": int64(7), "c": "99", "d": "abc"}
	if jsonI64(m, "a") != 42 {
		t.Error("a")
	}
	if jsonI64(m, "b") != 7 {
		t.Error("b")
	}
	if jsonI64(m, "c") != 99 {
		t.Error("c")
	}
	if jsonI64(m, "d") != 0 {
		t.Error("d")
	}
	if jsonI64(m, "missing") != 0 {
		t.Error("missing")
	}
}
func TestJsonStr(t *testing.T) {
	m := map[string]any{"a": "hello", "b": 42, "c": true}
	if jsonStr(m, "a") != "hello" {
		t.Error("a")
	}
	if jsonStr(m, "b") != "" {
		t.Error("b")
	}
	if jsonStr(m, "c") != "" {
		t.Error("c")
	}
	if jsonStr(m, "missing") != "" {
		t.Error("missing")
	}
	if jsonStr(m, "missing", "a") != "hello" {
		t.Error("fallback")
	}
}
func TestNextCheckinTime(t *testing.T) {
	// 07:00 → next 08:00
	morning := time.Date(2026, 7, 24, 7, 0, 0, 0, time.UTC)
	got := nextCheckinTime(morning)
	if got.Hour() != 8 {
		t.Fatalf("want 8, got %v", got.Hour())
	}
	// 10:00 → next 12:00
	noon := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	got = nextCheckinTime(noon)
	if got.Hour() != 12 {
		t.Fatalf("want 12, got %v", got.Hour())
	}
	// 22:00 → next day 00:00
	late := time.Date(2026, 7, 24, 22, 0, 0, 0, time.UTC)
	got = nextCheckinTime(late)
	if got.Hour() != 0 {
		t.Fatalf("want 0, got %v", got.Hour())
	}
	if got.Day() != 25 {
		t.Fatalf("want next day, got %v", got.Day())
	}
}
