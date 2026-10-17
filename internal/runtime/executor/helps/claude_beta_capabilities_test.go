package helps

import "testing"

func TestMergeClaudeBetaCapabilities(t *testing.T) {
	got := MergeClaudeBetaCapabilities("baseline,, review ", "review,unknown,UNKNOWN", []string{"unknown, body", "", "body"})
	if got != "baseline,review,unknown,UNKNOWN,body" {
		t.Fatalf("got %q", got)
	}
	if MergeClaudeBetaCapabilities(" ", "", nil) != "" {
		t.Fatal("empty input added a capability")
	}
}
