package shell

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCapture_Success(t *testing.T) {
	out, err := RunCapture(Command{
		Binary: "echo",
		Args:   []string{"hello world"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world\n", string(out))
}

func TestRunCapture_Failure(t *testing.T) {
	_, err := RunCapture(Command{
		Binary: "false",
	})
	assert.Error(t, err)
}

func TestRunCapture_EmptyBinary(t *testing.T) {
	_, err := RunCapture(Command{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "No command specified")
}
