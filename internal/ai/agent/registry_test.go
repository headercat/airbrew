package agent

import (
	"reflect"
	"testing"
)

func TestNormalizeAgentInputTrimsAndDeduplicatesTools(t *testing.T) {
	got, err := normalize(Input{
		Name:         "  Helper  ",
		Description:  "  Useful for quick replies.  ",
		Model:        "  fake-1  ",
		SystemPrompt: "  Be concise.  ",
		Tools:        []string{" clock ", "echo", "clock", ""},
		MaxTurns:     3,
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got.Name != "Helper" || got.Description != "Useful for quick replies." {
		t.Fatalf("text fields were not trimmed: %+v", got)
	}
	if got.Model != "fake-1" || got.SystemPrompt != "Be concise." {
		t.Fatalf("model/system fields were not trimmed: %+v", got)
	}
	if !reflect.DeepEqual(got.Tools, []string{"clock", "echo"}) {
		t.Fatalf("tools = %+v", got.Tools)
	}
}

func TestNormalizeAgentInputBoundsTextFields(t *testing.T) {
	if _, err := normalize(Input{Name: "n", Model: string(make([]byte, 161))}); err == nil {
		t.Fatalf("expected model length error")
	}
	if _, err := normalize(Input{
		Name:        "n",
		Model:       "fake-1",
		Description: string(make([]byte, 513)),
	}); err == nil {
		t.Fatalf("expected description length error")
	}
}
