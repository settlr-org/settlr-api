package httpx

import "encoding/json"

// Payload marshals an activity-event payload. Values are JSON-encoded properly, so user text
// (e.g. a description containing quotes) can never produce invalid JSONB.
func Payload(fields map[string]any) json.RawMessage {
	b, err := json.Marshal(fields)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// RawJSON exposes a stored JSONB column as an embedded JSON value in responses. Without it a
// []byte field is serialised by encoding/json as a base64 string.
func RawJSON(b []byte) json.RawMessage {
	if len(b) == 0 || !json.Valid(b) {
		return json.RawMessage(`{}`)
	}
	return b
}
