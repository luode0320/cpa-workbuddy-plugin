package cursorsession

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultTTL        = 15 * time.Minute
	DefaultMaxEntries = 64
	DefaultMaxBytes   = 16 * 1024 * 1024
)

type Reason string

const (
	ReasonHit                  Reason = "hit"
	ReasonMissing              Reason = "missing"
	ReasonStored               Reason = "stored"
	ReasonExpired              Reason = "expired"
	ReasonEvicted              Reason = "evicted"
	ReasonIdentityMismatch     Reason = "identity_mismatch"
	ReasonModelMismatch        Reason = "model_mismatch"
	ReasonSessionMismatch      Reason = "session_mismatch"
	ReasonSystemMismatch       Reason = "system_mismatch"
	ReasonPrefixMismatch       Reason = "prefix_mismatch"
	ReasonMessageCountMismatch Reason = "message_count_mismatch"
	ReasonCompaction           Reason = "compaction"
	ReasonDecodeFailure        Reason = "decode_failure"
	ReasonOversized            Reason = "oversized"
	ReasonStaleCommit          Reason = "stale_commit"
	ReasonInvalidated          Reason = "invalidated"
)

type Key struct {
	accountIdentity string
	requestedModel  string
	stableSession   string
}

func NewKey(accountIdentity, requestedModel, stableSession string) (Key, error) {
	key := Key{
		accountIdentity: strings.TrimSpace(accountIdentity),
		requestedModel:  strings.TrimSpace(requestedModel),
		stableSession:   strings.TrimSpace(stableSession),
	}
	if key.accountIdentity == "" || key.requestedModel == "" || key.stableSession == "" {
		return Key{}, fmt.Errorf("checkpoint key requires account identity, requested model, and stable session")
	}
	return key, nil
}

type Match struct {
	SystemDigest        string
	PrefixDigest        string
	PrefixDigests       []string
	CoveredMessageCount int
	Compacted           bool
}

type Confirmed struct {
	ConversationID      string
	Checkpoint          []byte
	CoveredMessageCount int
	SystemDigest        string
	PrefixDigest        string
}

type Snapshot struct {
	ConversationID      string
	CoveredMessageCount int
	SystemDigest        string
	PrefixDigest        string
	Generation          uint64
	checkpoint          []byte
}

func (s Snapshot) CheckpointBytes() []byte {
	return append([]byte(nil), s.checkpoint...)
}

type Result struct {
	Reason         Reason
	Snapshot       Snapshot
	Evicted        int
	EvictionReason Reason
}

type Token struct {
	store      *Store
	key        Key
	generation uint64
}

type Options struct {
	TTL        time.Duration
	MaxEntries int
	MaxBytes   int
	Now        func() time.Time
}
