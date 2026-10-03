package signaling_test

import (
	"encoding/json"
	"testing"

	"github.com/rm4n0s/errors"

	"github.com/rm4n0s/desktop-mirror/internal/signaling"
)

func TestMessageValidate(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantTag string
	}{
		{"hello", `{"type":"hello","ua":"x"}`, ""},
		{"offer", `{"type":"offer","sdp":"v=0"}`, ""},
		{"answer", `{"type":"answer","sdp":"v=0"}`, ""},
		{"candidate", `{"type":"candidate","candidate":"candidate:1","sdpMid":"0","sdpMLineIndex":0}`, ""},
		{"end of candidates", `{"type":"candidate"}`, ""},
		{"bye", `{"type":"bye"}`, ""},
		{"offer without sdp", `{"type":"offer"}`, "SignalingMessageInvalid"},
		{"unknown type", `{"type":"explode"}`, "SignalingMessageInvalid"},
		{"missing type", `{}`, "SignalingMessageInvalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m signaling.Message
			if err := json.Unmarshal([]byte(tt.json), &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			err := m.Validate()
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T", err)
			}
			if !appErr.HasRoute("Message.Validate." + tt.wantTag) {
				t.Errorf("unexpected failure path: %s", appErr.Route())
			}
		})
	}
}
