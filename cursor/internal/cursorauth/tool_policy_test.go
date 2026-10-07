package cursorauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Credentials_preserve_tool_policy_through_roundtrip_and_refresh(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		name := "roundtrip"
		if refresh {
			name = "refresh"
		}
		t.Run(name, func(t *testing.T) {
			// Given
			current, err := ParseCredentials([]byte(`{"type":"cursor-provider","access_token":"access","refresh_token":"refresh","tool_loop_guard_tools":["todowrite","task_update"]}`))
			require.NoError(t, err)
			service := NewService(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access"}`))}, nil
			})}, DefaultEndpoints())

			// When
			updated := current
			if refresh {
				updated, err = service.Refresh(context.Background(), current)
				require.NoError(t, err)
			}
			raw, err := MarshalCredentials(updated)
			require.NoError(t, err)

			// Then
			var stored struct {
				Tools []string `json:"tool_loop_guard_tools"`
			}
			require.NoError(t, json.Unmarshal(raw, &stored))
			require.Equal(t, []string{"todowrite", "task_update"}, stored.Tools)
		})
	}
}

func Test_Credentials_reject_malformed_tool_policy(t *testing.T) {
	for _, policy := range []string{`true`, `"todowrite"`, `{"todowrite":true}`, `[123]`} {
		t.Run(policy, func(t *testing.T) {
			raw := []byte(`{"type":"cursor-provider","access_token":"access","refresh_token":"refresh","tool_loop_guard_tools":` + policy + `}`)

			_, err := ParseCredentials(raw)

			require.Error(t, err)
		})
	}
}
