package ledger

import (
	"encoding/json/jsontext"
	"errors"
	"testing"
)

func TestApplyUpdate(t *testing.T) {
	tests := []struct {
		name         string
		metadata     string
		in           UpdateInput
		nameRequired bool
		wantName     string
		wantDesc     string
		wantMetadata string
		wantErr      error
	}{
		{name: "no changes", metadata: `{"a":"1"}`, wantName: "n", wantDesc: "d", wantMetadata: `{"a":"1"}`},
		{name: "rename", metadata: `{}`, in: UpdateInput{Name: new("new"), Description: new("")}, wantName: "new", wantMetadata: `{}`},
		{
			name:         "merge patch",
			metadata:     `{"a":"1","b":{"c":"2","d":"3"},"e":[1,2]}`,
			in:           UpdateInput{Metadata: jsontext.Value(`{"a":null,"b":{"c":null,"x":"9"},"e":[3],"n":1e2}`)},
			wantName:     "n",
			wantDesc:     "d",
			wantMetadata: `{"b":{"d":"3","x":"9"},"e":[3],"n":100}`,
		},
		{name: "null clears metadata", metadata: `{"a":"1"}`, in: UpdateInput{Metadata: jsontext.Value(`null`)}, wantName: "n", wantDesc: "d", wantMetadata: `{}`},
		{name: "patch not an object", metadata: `{}`, in: UpdateInput{Metadata: jsontext.Value(`[1]`)}, wantErr: ErrInvalid},
		{name: "required name cleared", metadata: `{}`, in: UpdateInput{Name: new(" ")}, nameRequired: true, wantErr: ErrInvalid},
		{name: "optional name cleared", metadata: `{}`, in: UpdateInput{Name: new("")}, wantName: "", wantDesc: "d", wantMetadata: `{}`},
		{name: "NUL in metadata", metadata: `{}`, in: UpdateInput{Metadata: jsontext.Value(`{"a":"\u0000"}`)}, wantErr: ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, desc, metadata, err := applyUpdate("n", "d", jsontext.Value(tt.metadata), tt.in, tt.nameRequired)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if name != tt.wantName || desc != tt.wantDesc || !jsonEqual(metadata, jsontext.Value(tt.wantMetadata)) {
				t.Fatalf("got %q %q %s, want %q %q %s", name, desc, metadata, tt.wantName, tt.wantDesc, tt.wantMetadata)
			}
		})
	}
}
