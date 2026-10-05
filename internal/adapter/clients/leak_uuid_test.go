package clients

import "regexp"

// leakUUIDRe matches a UUID anywhere in a learner-facing string; the ritual
// step engine test asserts no raw ids leak into the composed step message.
var leakUUIDRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
