package notes

import "testing"

func TestAddAndList(t *testing.T) {
	store := NewStore()
	first, err := store.Add("buy milk", []string{"errand"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if first.ID == "" {
		t.Fatal("Add returned an empty ID")
	}
	if _, err := store.Add("read book", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := len(store.List()); got != 2 {
		t.Fatalf("List length = %d, want 2", got)
	}
}

func TestAddRequiresText(t *testing.T) {
	if _, err := NewStore().Add("", nil); err == nil {
		t.Fatal("Add accepted empty text")
	}
}
