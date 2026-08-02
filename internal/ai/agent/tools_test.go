package agent

import (
	"reflect"
	"testing"
)

func TestToolRegistryKeysAreSorted(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register("zeta", echoTool{})
	reg.Register("alpha", clockTool{})
	reg.Register("middle", echoTool{})

	got := reg.Keys()
	want := []string{"alpha", "middle", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %+v, want %+v", got, want)
	}
}

func TestToolRegistrySchemasForDeduplicatesKeys(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register("echo", echoTool{})
	reg.Register("clock", clockTool{})

	got := reg.SchemasFor([]string{"echo", "missing", "echo", "", "clock"})
	if len(got) != 2 {
		t.Fatalf("expected 2 schemas, got %+v", got)
	}
	if got[0].Name != "echo" || got[1].Name != "clock" {
		t.Fatalf("schemas not in requested order: %+v", got)
	}
}
