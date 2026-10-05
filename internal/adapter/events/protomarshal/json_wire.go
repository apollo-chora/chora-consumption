// json_wire.go - the JSON-wire encoder for the ADR-254 D4 caller-facing
// workflow REQUEST lanes (schemaless on the bus; the proto in chora-contracts is
// the struct source for both sides; the kennel decodes by field NAME).
package protomarshal

import (
	"encoding/json"
	"errors"
	"fmt"
)

// encodeJSONWire marshals the payload map as the message body. The envelope
// rides on the Pub/Sub attributes (outbox.BuildRow) AND the caller puts the
// envelope fields it wants in the body itself (tenant_id, gcid, turn_id ...),
// so nothing is added here: what the caller built is exactly what the kennel
// reads.
func encodeJSONWire(payload map[string]any) ([]byte, error) {
	if payload == nil {
		return nil, errors.New("protomarshal: JSON-wire payload is nil")
	}
	bz, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("protomarshal: JSON-wire marshal: %w", err)
	}
	return bz, nil
}
