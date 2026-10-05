// served_id.go — CHO-2244 (SECURITY, amends WS-C7 Slice F): the single option-id
// derivation SHARED by the serve door and the answers door.
//
// Slice F stripped the answer-revealing FIELDS but shipped the qgen option ids
// and the qgen option ORDER verbatim. Both correlate with the answer:
//
//   - ids — a live batch minted opt_1_correct / opt_1_distractor1..3;
//   - order — measured over the live bank 2026-07-17, the is_correct option
//     sits at stored index 0 in 38 of 40 questions (95%), and the FE renders in
//     payload order with positional A/B/C/D markers and never shuffles.
//
// The order leak dominates, and it is why the ids the original story called
// neutral (opt_1, opt_1a) are not neutral either — they encode position.
//
// # Why the id is derived from display content
//
// servedOptionID hashes ONLY what the learner already sees: the question index
// plus the option's own display text. That is the whole security argument — the
// id is a function of bytes that are on screen anyway, so it can be recomputed
// by anyone and still reveals nothing. What it does NOT depend on is the two
// things that DO correlate with the answer: the stored option_id and the stored
// option index. Neither is recoverable from the serve.
//
// This is why the derivation needs no secret. Hashing the stored option_id
// would be brute-forceable (the id vocabulary is tiny and guessable — one leaked
// batch teaches the convention), so it would demand a real key; hashing the
// stored index would hand back position 0 directly. Display content is immune to
// both: an attacker who "breaks" the hash learns only the label they were
// already served.
//
// Sorting the options by this id is then a free, deterministic shuffle: the hash
// is uniform over display content, which is independent of correctness, so the
// served position carries no signal. Determinism keeps repeat serves stable
// (D13's "generate once, retrieve forever") and keeps the FE's `track` identity
// steady across re-polls.
package campaignquestion

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strings"
)

// servedOptionIDLen is the hex width of a served option id. 16 hex chars = 64
// bits — collision-free in practice for a handful of options per question, and
// a genuine collision is caught and failed loud rather than silently served.
const servedOptionIDLen = 16

// servedOptionID derives the opaque id the serve door ships for one option and
// the answers door grades against. Pure + deterministic: same display content →
// same id, on both doors, forever.
//
// Inputs are the option's display fields as stored. They are trimmed first so
// the id-space matches what the learner actually reads (the FE trims too) — two
// options whose rendered text is identical therefore collide, which is exactly
// the ambiguity the callers fail loud on.
func servedOptionID(questionIndex int, label, text string) string {
	h := sha256.New()
	// The question index is a domain separator: the same label under two
	// different questions gets different ids. It is public — the FE already
	// posts it back as question_index.
	writeUint64(h, uint64(questionIndex))
	// Length-prefixed so the fields cannot be concatenated ambiguously
	// (label="a",text="b" must not hash like label="ab",text="").
	writeLenPrefixed(h, strings.TrimSpace(label))
	writeLenPrefixed(h, strings.TrimSpace(text))
	return hex.EncodeToString(h.Sum(nil))[:servedOptionIDLen]
}

// hasDisplayContent reports whether an option carries anything a learner could
// read and click. An option without it cannot be rendered, distinguished, or
// keyed from what is on screen — callers fail loud rather than fall back to the
// stored id or the stored index, both of which leak the answer.
func hasDisplayContent(label, text string) bool {
	return strings.TrimSpace(label) != "" || strings.TrimSpace(text) != ""
}

func writeUint64(h hash.Hash, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	h.Write(b[:])
}

func writeLenPrefixed(h hash.Hash, s string) {
	writeUint64(h, uint64(len(s)))
	h.Write([]byte(s))
}
