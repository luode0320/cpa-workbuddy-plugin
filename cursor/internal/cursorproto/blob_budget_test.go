package cursorproto

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Blob_budget_charges_keys_and_preserves_value_on_rejected_overwrite(t *testing.T) {
	store := NewBlobStore()
	key := []byte("opaque\x00key")
	_, _, err := store.HandleServerMessage(encodeKVServerMessage(t, 1, "set_blob_args", key, []byte("old")))
	require.NoError(t, err)

	_, handled, err := store.HandleServerMessage(encodeKVServerMessage(t, 2, "set_blob_args", key, bytes.Repeat([]byte("x"), maxBlobEntryBytes)))

	require.True(t, handled)
	require.ErrorIs(t, err, ErrBlobCapacity)
	reply, _, err := store.HandleServerMessage(encodeKVServerMessage(t, 3, "get_blob_args", key, nil))
	require.NoError(t, err)
	requireKVReply(t, reply, 3, "get_blob_result", []byte("old"))
}

func Test_Blob_budget_counts_new_keys_once_and_reclaims_shrunk_values(t *testing.T) {
	store := NewBlobStore()
	key := []byte("key")
	require.NoError(t, store.set(key, []byte("longer")))

	require.NoError(t, store.set(key, []byte("x")))

	require.Equal(t, 4, store.totalBytes)
	require.Len(t, store.blobs, 1)
}

func Test_Blob_budget_rejects_aggregate_keys_with_empty_values(t *testing.T) {
	store := NewBlobStore()
	key := bytes.Repeat([]byte("a"), maxBlobEntryBytes)
	for index := range 4 {
		key[0] = byte(index)
		require.NoError(t, store.set(key, nil))
	}
	key[0] = 5

	err := store.set(key, nil)

	require.ErrorIs(t, err, ErrBlobCapacity)
	require.Len(t, store.blobs, 4)
	require.Equal(t, maxBlobTotalBytes, store.totalBytes)
}
