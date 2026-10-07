package plugin

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const requestCompletionTestWindowLoad = 1_000_000

func TestRequestCompletionFilter_StaysWithinMillionEventWindowBudget(t *testing.T) {
	filter := newRequestCompletionFilter()
	now := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)

	firstWindowFalsePositives := addUniqueRequestCompletions(filter, "first-", requestCompletionTestWindowLoad, now)
	now = now.Add(requestCompletionWindow + time.Second)
	secondWindowFalsePositives := addUniqueRequestCompletions(filter, "second-", requestCompletionTestWindowLoad, now)
	nextWindowFalsePositives := countUniqueRequestCompletionMatches(filter, "next-", requestCompletionTestWindowLoad, now)

	require.LessOrEqual(t, firstWindowFalsePositives, 1)
	require.LessOrEqual(t, secondWindowFalsePositives, 1)
	require.LessOrEqual(t, nextWindowFalsePositives, 1)
}

func TestRequestCompletionFilter_UsesFixedSixteenMiBBudget(t *testing.T) {
	filter := newRequestCompletionFilter()

	require.Len(t, filter.current, requestCompletionWords)
	require.Len(t, filter.previous, requestCompletionWords)
	require.Equal(t, 16<<20, (len(filter.current)+len(filter.previous))*8)
}

func TestRequestCompletionFilter_AdvertisedFalsePositiveBound(t *testing.T) {
	bits := float64(requestCompletionFilterBits)
	hashes := float64(requestCompletionHashes)
	insertions := float64(requestCompletionTestWindowLoad)
	perWindow := math.Pow(1-math.Pow(1-1/bits, hashes*insertions), hashes)

	require.Less(t, 2*perWindow, 6e-8)
}

func TestRequestCompletionFilter_DoesNotMixBitsAcrossWindows(t *testing.T) {
	filter := newRequestCompletionFilter()
	key := requestAuthKey("split-across-windows")
	for index, position := range requestCompletionPositions(key) {
		words := filter.current
		if index%2 == 1 {
			words = filter.previous
		}
		words[position/64] |= uint64(1) << (position % 64)
	}

	require.False(t, filter.contains(key, time.Now()))
}

func TestUsageStore_ProductionClockRetainsMonotonicReading(t *testing.T) {
	store := newUsageStore()

	require.Contains(t, store.now().String(), "m=+")
}

func addUniqueRequestCompletions(filter *requestCompletionFilter, prefix string, count int, now time.Time) int {
	falsePositives := 0
	for index := 0; index < count; index++ {
		key := requestAuthKey(prefix + strconv.Itoa(index))
		if filter.contains(key, now) {
			falsePositives++
			continue
		}
		filter.add(key, now)
	}
	return falsePositives
}

func countUniqueRequestCompletionMatches(filter *requestCompletionFilter, prefix string, count int, now time.Time) int {
	matches := 0
	for index := 0; index < count; index++ {
		if filter.contains(requestAuthKey(prefix+strconv.Itoa(index)), now) {
			matches++
		}
	}
	return matches
}
