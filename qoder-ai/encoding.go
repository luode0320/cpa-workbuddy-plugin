// encoding.go implements Qoder's custom base64 variant used for request bodies.
//
// Algorithm (mirrors /tmp/cpa-plugin/qoderwork/reference_impl.py):
//  1. std = standard base64(plain)
//  2. rearranged = std[n-a:] + std[a:n-a] + std[:a] where a = n/3
//  3. map each char from std alphabet to custom alphabet; '=' → '$'
package main

import (
	"encoding/base64"
	"strings"
)

const (
	qoderCustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	qoderStdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	qoderCustomPad      = '$'
)

var (
	qoderS2C [128]byte // std char → custom char (0 = invalid)
	qoderC2S [128]byte // custom char → std char (0 = invalid)
)

func init() {
	for i := 0; i < 64; i++ {
		qoderS2C[qoderStdAlphabet[i]] = qoderCustomAlphabet[i]
		qoderC2S[qoderCustomAlphabet[i]] = qoderStdAlphabet[i]
	}
	qoderS2C['='] = qoderCustomPad
	qoderC2S[qoderCustomPad] = '='
}

// qoderEncode encodes plaintext to Qoder's custom base64 variant.
func qoderEncode(plain []byte) string {
	std := base64.StdEncoding.EncodeToString(plain)
	n := len(std)
	a := n / 3
	rearranged := std[n-a:] + std[a:n-a] + std[:a]
	var sb strings.Builder
	sb.Grow(n)
	for i := 0; i < n; i++ {
		c := rearranged[i]
		if c >= 128 || qoderS2C[c] == 0 {
			// Should never happen for valid base64 input.
			sb.WriteByte(c)
			continue
		}
		sb.WriteByte(qoderS2C[c])
	}
	return sb.String()
}
