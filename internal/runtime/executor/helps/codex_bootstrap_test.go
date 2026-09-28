package helps

import "testing"

func TestCodexBootstrapByteAndEventBudgets(t *testing.T) {
	var b CodexBootstrapBudget
	if ok, _ := b.Accept([][]byte{make([]byte, CodexBootstrapMaxBytes-1), {0}}); !ok {
		t.Fatal("exact byte boundary rejected")
	}
	if ok, why := b.Accept([][]byte{{0}}); ok || why != "buffer_byte_limit" {
		t.Fatal("one-byte overflow retained")
	}
	if b.bytes != CodexBootstrapMaxBytes || b.events != 1 {
		t.Fatal("rejected event mutated buffer")
	}
	b = CodexBootstrapBudget{}
	if ok, why := b.Accept([][]byte{make([]byte, CodexBootstrapMaxBytes+1)}); ok || why != "buffer_byte_limit" {
		t.Fatal("large event retained")
	}
	for i := 0; i < CodexBootstrapMaxEvents; i++ {
		if ok, _ := b.Accept(nil); !ok {
			t.Fatal("premature event limit")
		}
	}
	if ok, why := b.Accept(nil); ok || why != "buffer_event_limit" {
		t.Fatal("empty translations bypass event limit")
	}
}
