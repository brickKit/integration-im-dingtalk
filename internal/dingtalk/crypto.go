package dingtalk

// The callbacks' signature and encryption (docs/design.md ID06), modelled on DingTalk's event subscription (HTTP push,
// as of 2026-11; to be checked against DingTalk's test organisation, the design's open questions): the signature is
// the SHA-1 of the token, the timestamp, the nonce and the ciphertext sorted and joined; the ciphertext is AES-256-CBC
// with the key base64(aesKey+"="), the IV its first 16 bytes, PKCS#7 padding to 32 bytes, over 16 random bytes, the
// message's length (4 bytes, big endian), the message and the application key.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

// Sign is a callback's signature.
func Sign(token, timestamp, nonce, encrypt string) string {
	parts := []string{token, timestamp, nonce, encrypt}
	sort.Strings(parts)
	sum := sha1.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(sum[:])
}

// Verify compares a callback's signature in constant time.
func Verify(token, timestamp, nonce, encrypt, signature string) bool {
	return subtle.ConstantTimeCompare([]byte(Sign(token, timestamp, nonce, encrypt)), []byte(signature)) == 1
}

func key(aesKey string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(aesKey + "=")
	if err != nil || len(k) != 32 {
		return nil, errors.New("dingtalk: the AES key is not 43 base64 characters")
	}
	return k, nil
}

const block = 32

// Encrypt is a message as DingTalk's callbacks carry it (and as the answer to one).
func Encrypt(aesKey, appKey string, msg []byte) (string, error) {
	k, err := key(aesKey)
	if err != nil {
		return "", err
	}
	var plain bytes.Buffer
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	plain.Write(random)
	_ = binary.Write(&plain, binary.BigEndian, uint32(len(msg)))
	plain.Write(msg)
	plain.WriteString(appKey)
	pad := block - plain.Len()%block
	plain.Write(bytes.Repeat([]byte{byte(pad)}, pad))
	c, _ := aes.NewCipher(k)
	out := make([]byte, plain.Len())
	cipher.NewCBCEncrypter(c, k[:16]).CryptBlocks(out, plain.Bytes())
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt opens a callback's ciphertext; the message must end with the application key.
func Decrypt(aesKey, appKey, encrypt string) ([]byte, error) {
	k, err := key(aesKey)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(encrypt)
	if err != nil || len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return nil, errors.New("dingtalk: the ciphertext is not a whole number of blocks")
	}
	c, _ := aes.NewCipher(k)
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(c, k[:16]).CryptBlocks(plain, raw)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > block || pad > len(plain) {
		return nil, errors.New("dingtalk: bad padding")
	}
	plain = plain[:len(plain)-pad]
	if len(plain) < 20 {
		return nil, errors.New("dingtalk: the message is too short")
	}
	n := int(binary.BigEndian.Uint32(plain[16:20]))
	if n < 0 || 20+n > len(plain) {
		return nil, errors.New("dingtalk: bad message length")
	}
	if string(plain[20+n:]) != appKey {
		return nil, errors.New("dingtalk: the message is for another application")
	}
	return plain[20 : 20+n], nil
}
