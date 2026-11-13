package dingtalk

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const aesKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG" // 43 characters

// A message encrypted for the application opens again; for another application, with a changed byte or another key it
// does not; the signature is over the four values sorted and fails when one changes.
func TestTheCallbackCrypto(t *testing.T) {
	enc, err := Encrypt(aesKey, "app-1", []byte(`{"a":1}`))
	require.NoError(t, err)
	msg, err := Decrypt(aesKey, "app-1", enc)
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(msg))
	_, err = Decrypt(aesKey, "app-2", enc)
	assert.Error(t, err, "another application")
	_, err = Decrypt("0bcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", "app-1", enc)
	assert.Error(t, err, "another key")
	_, err = Decrypt(aesKey, "app-1", "not base64!")
	assert.Error(t, err)

	sig := Sign("tok", "1700000000", "n1", enc)
	assert.True(t, Verify("tok", "1700000000", "n1", enc, sig))
	assert.Equal(t, sig, Sign("n1", "tok", enc, "1700000000"), "sorted")
	assert.False(t, Verify("tok", "1700000001", "n1", enc, sig))
	assert.False(t, Verify("other", "1700000000", "n1", enc, sig))
}
