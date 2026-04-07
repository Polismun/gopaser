package main

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// Port of csgo-sharecode npm (decodeMatchShareCode only).
// Decodes a CS2 match sharecode into matchId, reservationId, tvPort.

const sharecodeDictionary = "ABCDEFGHJKLMNOPQRSTUVWXYZabcdefhijkmnopqrstuvwxyz23456789"

var sharecodePattern = regexp.MustCompile(`^CSGO(-?[\w]{5}){5}$`)

// DecodedSharecode holds the three fields extracted from a CS2 match sharecode.
type DecodedSharecode struct {
	MatchID       uint64
	ReservationID uint64
	TVPort        uint16
}

// decodeSharecode decodes a CS2 match sharecode (e.g. "CSGO-xxxxx-xxxxx-xxxxx-xxxxx-xxxxx").
func decodeSharecode(code string) (DecodedSharecode, error) {
	if !sharecodePattern.MatchString(code) {
		return DecodedSharecode{}, fmt.Errorf("invalid sharecode format: %s", code)
	}

	// Strip "CSGO" prefix and dashes
	clean := strings.ReplaceAll(strings.Replace(code, "CSGO", "", 1), "-", "")

	// Reverse the characters
	chars := []byte(clean)
	for i, j := 0, len(chars)-1; i < j; i, j = i+1, j-1 {
		chars[i], chars[j] = chars[j], chars[i]
	}

	// Convert from custom base to big integer
	dictLen := big.NewInt(int64(len(sharecodeDictionary)))
	total := big.NewInt(0)
	for _, ch := range chars {
		idx := strings.IndexByte(sharecodeDictionary, ch)
		if idx < 0 {
			return DecodedSharecode{}, errors.New("invalid character in sharecode")
		}
		total.Mul(total, dictLen)
		total.Add(total, big.NewInt(int64(idx)))
	}

	// Convert to 18-byte hex string (padded)
	hexStr := fmt.Sprintf("%036x", total)
	bytes := hexToBytes(hexStr)
	if len(bytes) < 18 {
		return DecodedSharecode{}, errors.New("decoded sharecode too short")
	}

	// Extract fields (little-endian: reverse each slice)
	matchID := bytesToUint64(reverseBytes(bytes[0:8]))
	reservationID := bytesToUint64(reverseBytes(bytes[8:16]))
	tvPort := uint16(bytesToUint64(reverseBytes(bytes[16:18])))

	return DecodedSharecode{
		MatchID:       matchID,
		ReservationID: reservationID,
		TVPort:        tvPort,
	}, nil
}

func hexToBytes(h string) []byte {
	b := make([]byte, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		fmt.Sscanf(h[i:i+2], "%02x", &b[i/2])
	}
	return b
}

func reverseBytes(b []byte) []byte {
	r := make([]byte, len(b))
	for i, j := 0, len(b)-1; i <= j; i, j = i+1, j-1 {
		r[i], r[j] = b[j], b[i]
	}
	return r
}

func bytesToUint64(b []byte) uint64 {
	var n uint64
	for _, v := range b {
		n = n<<8 | uint64(v)
	}
	return n
}
