package githubrun

import (
	"testing"

	"github.com/kiwi-init/greenrun/internal/mask"
	"github.com/stretchr/testify/require"
)

func TestParseAnnotationsMasksSecrets(t *testing.T) {
	raw := []byte(`[[{
		"path":"src/main.go",
		"start_line":12,
		"start_column":4,
		"annotation_level":"failure",
		"title":"Type error",
		"message":"token secret-value is invalid",
		"raw_details":"details"
	}]]`)

	diagnostics, err := parseAnnotations(raw, mask.New("secret-value"))

	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	require.Equal(t, "src/main.go", diagnostics[0].File)
	require.Equal(t, 12, diagnostics[0].Line)
	require.Equal(t, 4, diagnostics[0].Column)
	require.NotContains(t, diagnostics[0].Message, "secret-value")
	require.Contains(t, diagnostics[0].Message, "***")
}
