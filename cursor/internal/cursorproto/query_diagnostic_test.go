package cursorproto

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func Test_QueryShape_records_only_top_level_field_numbers(t *testing.T) {
	cases := []struct {
		name, raw, eventType string
		fields               []protoreflect.FieldNumber
	}{
		{"empty", "3a00", "interaction_query", nil},
		{"id_only", "3a02082a", "interaction_query", []protoreflect.FieldNumber{1}},
		{"unknown_nine", "3a04082a4a00", "interaction_query", []protoreflect.FieldNumber{1, 9}},
		{"known_web", "3a04082a1200", "interaction_query.web_search_request_query", []protoreflect.FieldNumber{1, 2}},
		{"wrong_wire_type", "3a04082a1001", "interaction_query", []protoreflect.FieldNumber{1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			raw, err := hex.DecodeString(tc.raw)
			require.NoError(t, err)
			// When
			event, err := DecodeServerEvent(raw)
			// Then
			require.NoError(t, err)
			require.Equal(t, EventIgnored, event.Kind)
			require.Equal(t, tc.eventType, event.Type)
			require.NotNil(t, event.Query)
			require.Equal(t, tc.fields, event.Query.Fields)
			require.False(t, event.Query.Truncated)
		})
	}
}

func Test_QueryShape_bounds_fields_and_excludes_values(t *testing.T) {
	// Given
	const secret = "DIAGNOSTIC_CANARY_SECRET_QUERY_CONTENT"
	var query []byte
	for field := protowire.Number(90); field < 110; field++ {
		query = protowire.AppendTag(query, field, protowire.BytesType)
		query = protowire.AppendString(query, secret)
	}
	raw := protowire.AppendTag(nil, 7, protowire.BytesType)
	raw = protowire.AppendBytes(raw, query)
	// When
	event, err := DecodeServerEvent(raw)
	// Then
	require.NoError(t, err)
	require.NotNil(t, event.Query)
	require.Equal(t, []protoreflect.FieldNumber{90, 91, 92, 93, 94, 95, 96, 97}, event.Query.Fields)
	require.True(t, event.Query.Truncated)
	encoded, err := json.Marshal(event.Query)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
}

func Test_QueryShape_truncates_only_after_eight_field_occurrences(t *testing.T) {
	for _, count := range []int{8, 9} {
		// Given
		var query []byte
		for index := 0; index < count; index++ {
			query = protowire.AppendTag(query, 99, protowire.VarintType)
			query = protowire.AppendVarint(query, 123456789)
		}
		raw := protowire.AppendBytes(protowire.AppendTag(nil, 7, protowire.BytesType), query)
		// When
		event, err := DecodeServerEvent(raw)
		// Then
		require.NoError(t, err)
		require.Equal(t, []protoreflect.FieldNumber{99, 99, 99, 99, 99, 99, 99, 99}, event.Query.Fields)
		require.Equal(t, count > 8, event.Query.Truncated)
	}
}
