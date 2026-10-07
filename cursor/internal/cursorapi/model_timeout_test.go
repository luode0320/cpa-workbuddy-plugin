package cursorapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_Model_discovery_has_its_own_bounded_deadline(t *testing.T) {
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		deadline, bounded := request.Context().Deadline()
		require.True(t, bounded, "model discovery must not inherit an unlimited lifetime")
		require.Positive(t, time.Until(deadline))
		require.LessOrEqual(t, time.Until(deadline), 15*time.Second)
		return responseWithBody(http.StatusOK, modelsResponse("auto")), nil
	})
	client, err := NewClient(Config{BaseURL: "https://api2.cursor.sh", HTTPClient: &http.Client{Transport: transport}})
	require.NoError(t, err)

	models, err := client.DiscoverModels(context.Background(), "synthetic")

	require.NoError(t, err)
	require.Equal(t, []string{"auto"}, models)
}
