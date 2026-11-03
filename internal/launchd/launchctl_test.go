package launchd

import "testing"

func TestDomainFor(t *testing.T) {
	if got := DomainFor(0); got != "system" {
		t.Fatalf("DomainFor(0) = %q, want system", got)
	}
	if got := DomainFor(501); got != "gui/501" {
		t.Fatalf("DomainFor(501) = %q, want gui/501", got)
	}
}
