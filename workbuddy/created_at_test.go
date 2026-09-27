package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// testJWT builds a three-segment token with the given raw payload. The
// signature is a placeholder — parseCreatedAtFromAccessToken never verifies it.
func testJWT(payload string) string {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return enc(`{"alg":"RS256","typ":"JWT"}`) + "." + enc(payload) + "." + enc("not-a-real-signature")
}

func TestParseCreatedAtFromAccessToken(t *testing.T) {
	want := time.Unix(1756312200, 0).UTC()
	// A token whose payload is standard-base64 padded ("=" suffix). Some
	// providers emit padded JWT segments; the parser must strip and accept it.
	padded := testJWT(`{"auth_time":1756312200}`)
	seg := strings.Split(padded, ".")
	for len(seg[1])%4 != 0 {
		seg[1] += "="
	}
	paddedWithPadding := seg[0] + "." + seg[1] + "." + seg[2]
	cases := []struct {
		name string
		tok  string
		want time.Time
		ok   bool
	}{
		{"auth_time", testJWT(`{"sub":"u1","iat":1790000000,"auth_time":1756312200,"exp":1799999999}`), want, true},
		{"auth_time_padded_segment", paddedWithPadding, want, true},
		{"missing_auth_time", testJWT(`{"sub":"u1","iat":1790000000}`), time.Time{}, false},
		{"zero_auth_time", testJWT(`{"auth_time":0}`), time.Time{}, false},
		{"negative_auth_time", testJWT(`{"auth_time":-1}`), time.Time{}, false},
		{"not_json_payload", "a.bbb.ccc", time.Time{}, false},
		{"two_segments", "aaa.bbb", time.Time{}, false},
		{"empty", "", time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseCreatedAtFromAccessToken(tc.tok)
			if ok != tc.ok {
				t.Fatalf("ok=%v; want %v (token=%q)", ok, tc.ok, tc.tok)
			}
			if !ok {
				return
			}
			if !got.Equal(tc.want) {
				t.Fatalf("got %s; want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
			if got.Location() != time.UTC {
				t.Fatalf("location=%v; want UTC", got.Location())
			}
		})
	}
}
