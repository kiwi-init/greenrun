package mask

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaskerMasksLongestValuesAndDynamicMasks(t *testing.T) {
	masker := New("token", "token-with-suffix")
	require.Equal(t, "*** ***", masker.Apply("token-with-suffix token"))

	masker.Observe("::add-mask::dynamic-secret")
	require.Equal(t, "value=***", masker.Apply("value=dynamic-secret"))
}
