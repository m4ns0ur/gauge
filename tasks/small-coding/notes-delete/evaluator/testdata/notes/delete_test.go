package notes

import "testing"

func TestDeleteNote(t *testing.T) {
	store := NewStore()
	first, _ := store.Add("first", nil)
	second, _ := store.Add("second", nil)
	third, _ := store.Add("third", nil)

	if err := store.Delete(second.ID); err != nil {
		t.Fatalf("Delete(%q): %v", second.ID, err)
	}
	remaining := store.List()
	if len(remaining) != 2 || remaining[0].ID != first.ID || remaining[1].ID != third.ID {
		t.Fatalf("remaining notes = %+v, want %q then %q", remaining, first.ID, third.ID)
	}
	if _, ok := store.Find(second.ID); ok {
		t.Errorf("Find still returns deleted note %q", second.ID)
	}

	if err := store.Delete("missing"); err == nil {
		t.Fatal("Delete accepted an unknown ID")
	}
	if got := len(store.List()); got != 2 {
		t.Errorf("failed Delete changed the store: %d notes", got)
	}
}
