package httpx

import (
	"encoding/json"
	"testing"
)

func TestPayloadEscapesUserText(t *testing.T) {
	p := Payload(map[string]any{"description": `6" pizza \ "large"`, "amount": int64(1200)})
	var got map[string]any
	if err := json.Unmarshal(p, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v (%s)", err, p)
	}
	if got["description"] != `6" pizza \ "large"` {
		t.Fatalf("description = %v", got["description"])
	}
}

func TestRawJSONEmbedsObjectNotBase64(t *testing.T) {
	out, err := json.Marshal(map[string]any{"payload": RawJSON([]byte(`{"expense_id":"x"}`))})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"payload":{"expense_id":"x"}}` {
		t.Fatalf("got %s", out)
	}
	if string(RawJSON(nil)) != `{}` || string(RawJSON([]byte("not json"))) != `{}` {
		t.Fatal("invalid input should become {}")
	}
}
