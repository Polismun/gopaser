package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// AES-256-GCM constants matching authCodeCrypto.ts
const (
	aesIVLen  = 12
	aesKeyLen = 32
	aesTagLen = 16
)

var hexKeyRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// decryptAuthCode decrypts an AES-256-GCM encrypted auth code.
// ciphertextB64: base64 ciphertext with GCM auth tag appended (last 16 bytes).
// ivB64: base64-encoded 12-byte IV.
// Returns the plaintext auth code string.
func decryptAuthCode(ciphertextB64, ivB64 string) (string, error) {
	key, err := getAESKey()
	if err != nil {
		return "", err
	}

	iv, err := base64.StdEncoding.DecodeString(ivB64)
	if err != nil {
		return "", fmt.Errorf("bad IV base64: %w", err)
	}
	if len(iv) != aesIVLen {
		return "", fmt.Errorf("IV length %d, expected %d", len(iv), aesIVLen)
	}

	combined, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", fmt.Errorf("bad ciphertext base64: %w", err)
	}
	if len(combined) < aesTagLen {
		return "", errors.New("ciphertext too short")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// GCM standard: ciphertext + tag concatenated, then passed as one to Open with nil additional data
	// Go's GCM.Open expects nonce + ciphertext+tag, but we pass nonce separately
	plaintext, err := gcm.Open(nil, iv, combined, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed (bad key or tampered): %w", err)
	}

	return string(plaintext), nil
}

func getAESKey() ([]byte, error) {
	secret := strings.TrimSpace(os.Getenv("STEAM_AUTHCODE_SECRET"))
	if secret == "" {
		return nil, errors.New("STEAM_AUTHCODE_SECRET not set")
	}

	// Accept hex (64 chars) or base64 (44 chars with '=' padding)
	var key []byte
	if hexKeyRe.MatchString(secret) {
		var err error
		key, err = hex.DecodeString(secret)
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		key, err = base64.StdEncoding.DecodeString(secret)
		if err != nil {
			return nil, err
		}
	}

	if len(key) != aesKeyLen {
		return nil, fmt.Errorf("key length %d, expected %d", len(key), aesKeyLen)
	}
	return key, nil
}
