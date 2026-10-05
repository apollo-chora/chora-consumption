// Package domain provides shared utilities for the consumption domain layer.
//
// uuidv7.go: A small, dependency-free UUIDv7 generator used by aggregate
// constructors. Per ddd-enforcement invariant #7, new tables use UUIDv7.
//
// UUIDv7 format (RFC 9562):
//
//	unix_ts_ms (48 bits) | ver (4) | rand_a (12) | var (2) | rand_b (62)
//
// This implementation uses crypto/rand for the entropy bits — sufficient for
// test + skeleton service. Production services may import google/uuid in M12.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewUUIDv7 returns a hyphenated v7 UUID string.
func NewUUIDv7() string {
	var b [16]byte
	// 48-bit big-endian Unix timestamp in milliseconds.
	tsMs := uint64(time.Now().UnixMilli())
	b[0] = byte(tsMs >> 40)
	b[1] = byte(tsMs >> 32)
	b[2] = byte(tsMs >> 24)
	b[3] = byte(tsMs >> 16)
	b[4] = byte(tsMs >> 8)
	b[5] = byte(tsMs)

	// 10 random bytes for the trailing entropy.
	if _, err := rand.Read(b[6:]); err != nil {
		// crypto/rand should never fail on supported platforms.
		// Fall back to nil bytes — still a valid (though degenerate) UUID.
		for i := 6; i < 16; i++ {
			b[i] = 0
		}
	}

	// Set version (7) in byte 6 high nibble.
	b[6] = (b[6] & 0x0F) | 0x70
	// Set variant (RFC 9562) in byte 8 high bits.
	b[8] = (b[8] & 0x3F) | 0x80

	return formatUUID(b)
}

func formatUUID(b [16]byte) string {
	var hexBuf [32]byte
	hex.Encode(hexBuf[:], b[:])
	// Insert hyphens at positions 8, 13, 18, 23.
	out := make([]byte, 36)
	copy(out[0:8], hexBuf[0:8])
	out[8] = '-'
	copy(out[9:13], hexBuf[8:12])
	out[13] = '-'
	copy(out[14:18], hexBuf[12:16])
	out[18] = '-'
	copy(out[19:23], hexBuf[16:20])
	out[23] = '-'
	copy(out[24:36], hexBuf[20:32])
	return string(out)
}
