// aliases.go — small type aliases shared by the S6.4 subscribers.
//
// We use these aliases to keep the public payload structs ergonomic in
// tests (which import `time.Time`) while concentrating the underlying
// import here for any future swap (e.g., to a `time.Time` wrapper that
// also tracks event-replay drift).
package subscribers

import "time"

// timeAlias is the underlying timestamp type for the
// `OccurredAt` field on subscriber payloads. Aliased so that test
// code can pass plain `time.Time` values directly.
type timeAlias = time.Time
