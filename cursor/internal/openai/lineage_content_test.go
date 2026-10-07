package openai

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Lineage_coalesces_only_equivalent_adjacent_text_parts(t *testing.T) {
	for _, role := range []string{"system", "developer", "user", "assistant", "tool"} {
		t.Run(role, func(t *testing.T) {
			parse := func(content string) ChatRequest {
				request, err := ParseChatRequest([]byte(fmt.Sprintf(`{"model":"auto","messages":[{"role":"user","content":"seed"},{"role":%q,"content":%s}]}`, role, content)))
				require.NoError(t, err)
				return request
			}
			original := parse(`"first\nsecond"`)
			equivalent := parse(`[{"type":"text","text":"first"},{"type":"text","text":"second"}]`)

			require.Equal(t, original.Prompt, equivalent.Prompt)
			require.Equal(t, original.System, equivalent.System)
			require.Equal(t, original.Lineage, equivalent.Lineage)
			for _, different := range []string{
				`"firstsecond"`, `"first second"`, `"second\nfirst"`, `"first\n\nsecond"`,
				`[{"type":"text","text":"first"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}},{"type":"text","text":"second"}]`,
			} {
				require.NotEqual(t, original.Lineage, parse(different).Lineage)
			}
		})
	}
}
