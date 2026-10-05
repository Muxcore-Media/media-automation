package automationv1

import "testing"

// TestDescriptorLoads guards against a corrupt raw descriptor in the
// generated code (init would panic before any test runs).
func TestDescriptorLoads(t *testing.T) {
	fd := File_proto_automationv1_automation_proto
	if got := fd.Services().Len(); got != 1 {
		t.Fatalf("services=%d want 1", got)
	}
	if got := fd.Services().Get(0).Methods().Len(); got != 14 {
		t.Fatalf("methods=%d want 14", got)
	}
}
