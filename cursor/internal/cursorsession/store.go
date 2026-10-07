package cursorsession

import (
	"container/list"
	"sync"
	"time"
)

type Store struct {
	mu             sync.Mutex
	ttl            time.Duration
	maxEntries     int
	maxBytes       int
	now            func() time.Time
	entries        map[Key]*entry
	nextGeneration uint64
	generations    map[Key]*generation
	generationLRU  list.List
	lru            list.List
	totalBytes     int
}

type generation struct {
	value   uint64
	element *list.Element
}

type entry struct {
	key        Key
	snapshot   Snapshot
	lastAccess time.Time
	element    *list.Element
}

func NewStore(options Options) *Store {
	ttl := options.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	maxEntries := options.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	maxBytes := options.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Store{
		ttl: ttl, maxEntries: maxEntries, maxBytes: maxBytes, now: now,
		entries: make(map[Key]*entry), nextGeneration: 1, generations: make(map[Key]*generation),
	}
}

func (s *Store) Start(key Key, match Match) (Token, Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := s.startToken(key)
	if match.Compacted {
		s.remove(key)
		return token, Result{Reason: ReasonCompaction}
	}
	current := s.entries[key]
	if current == nil {
		return token, Result{Reason: s.mismatchReason(key, s.now())}
	}
	now := s.now()
	if !now.Before(current.lastAccess.Add(s.ttl)) {
		s.remove(key)
		return token, Result{Reason: ReasonExpired}
	}
	if current.snapshot.SystemDigest != match.SystemDigest {
		s.remove(key)
		return token, Result{Reason: ReasonSystemMismatch}
	}
	if len(match.PrefixDigests) > 0 {
		covered := current.snapshot.CoveredMessageCount
		if covered <= 0 || covered > len(match.PrefixDigests) {
			s.remove(key)
			return token, Result{Reason: ReasonCompaction}
		}
		if current.snapshot.PrefixDigest != match.PrefixDigests[covered-1] {
			s.remove(key)
			return token, Result{Reason: ReasonPrefixMismatch}
		}
		current.lastAccess = now
		s.lru.MoveToFront(current.element)
		return token, Result{Reason: ReasonHit, Snapshot: cloneSnapshot(current.snapshot)}
	}
	if current.snapshot.PrefixDigest != match.PrefixDigest {
		s.remove(key)
		return token, Result{Reason: ReasonPrefixMismatch}
	}
	if current.snapshot.CoveredMessageCount != match.CoveredMessageCount {
		s.remove(key)
		return token, Result{Reason: ReasonMessageCountMismatch}
	}
	current.lastAccess = now
	s.lru.MoveToFront(current.element)
	return token, Result{Reason: ReasonHit, Snapshot: cloneSnapshot(current.snapshot)}
}

func (s *Store) Commit(token Token, confirmed Confirmed) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := s.generations[token.key]
	if token.store != s || latest == nil || latest.value != token.generation {
		return Result{Reason: ReasonStaleCommit}
	}
	if confirmed.ConversationID == "" || len(confirmed.Checkpoint) == 0 {
		return Result{Reason: ReasonDecodeFailure}
	}
	if len(confirmed.Checkpoint) > s.maxBytes {
		return Result{Reason: ReasonOversized}
	}
	s.remove(token.key)
	snapshot := Snapshot{
		ConversationID: confirmed.ConversationID, CoveredMessageCount: confirmed.CoveredMessageCount,
		SystemDigest: confirmed.SystemDigest, PrefixDigest: confirmed.PrefixDigest,
		Generation: token.generation, checkpoint: append([]byte(nil), confirmed.Checkpoint...),
	}
	current := &entry{key: token.key, snapshot: snapshot, lastAccess: s.now()}
	current.element = s.lru.PushFront(current)
	s.entries[token.key] = current
	s.totalBytes += len(snapshot.checkpoint)
	evicted := 0
	for len(s.entries) > s.maxEntries || s.totalBytes > s.maxBytes {
		oldest := s.lru.Back()
		if oldest == nil {
			break
		}
		s.remove(oldest.Value.(*entry).key)
		evicted++
	}
	result := Result{Reason: ReasonStored, Snapshot: cloneSnapshot(snapshot), Evicted: evicted}
	if evicted > 0 {
		result.EvictionReason = ReasonEvicted
	}
	return result
}

func (s *Store) Invalidate(key Key, reason Reason) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeGeneration(key)
	s.remove(key)
	if reason == "" {
		reason = ReasonInvalidated
	}
	return Result{Reason: reason}
}

func (s *Store) startToken(key Key) Token {
	s.removeGeneration(key)
	if s.nextGeneration == 0 {
		return Token{store: s, key: key}
	}

	value := s.nextGeneration
	if value == ^uint64(0) {
		s.nextGeneration = 0
	} else {
		s.nextGeneration++
	}
	latest := &generation{value: value}
	latest.element = s.generationLRU.PushFront(key)
	s.generations[key] = latest
	for len(s.generations) > s.maxEntries {
		oldest := s.generationLRU.Back()
		s.removeGeneration(oldest.Value.(Key))
	}
	return Token{store: s, key: key, generation: value}
}

func (s *Store) removeGeneration(key Key) {
	latest := s.generations[key]
	if latest == nil {
		return
	}
	delete(s.generations, key)
	s.generationLRU.Remove(latest.element)
}

func (s *Store) mismatchReason(key Key, now time.Time) Reason {
	identityMismatch := false
	modelMismatch := false
	sessionMismatch := false
	for existing := range s.entries {
		if !now.Before(s.entries[existing].lastAccess.Add(s.ttl)) {
			s.remove(existing)
			continue
		}
		if existing.requestedModel == key.requestedModel && existing.stableSession == key.stableSession && existing.accountIdentity != key.accountIdentity {
			identityMismatch = true
		}
		if existing.accountIdentity == key.accountIdentity && existing.stableSession == key.stableSession && existing.requestedModel != key.requestedModel {
			modelMismatch = true
		}
		if existing.accountIdentity == key.accountIdentity && existing.requestedModel == key.requestedModel && existing.stableSession != key.stableSession {
			sessionMismatch = true
		}
	}
	if identityMismatch {
		return ReasonIdentityMismatch
	}
	if modelMismatch {
		return ReasonModelMismatch
	}
	if sessionMismatch {
		return ReasonSessionMismatch
	}
	return ReasonMissing
}

func (s *Store) remove(key Key) {
	current := s.entries[key]
	if current == nil {
		return
	}
	delete(s.entries, key)
	s.lru.Remove(current.element)
	s.totalBytes -= len(current.snapshot.checkpoint)
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.checkpoint = append([]byte(nil), snapshot.checkpoint...)
	return snapshot
}
