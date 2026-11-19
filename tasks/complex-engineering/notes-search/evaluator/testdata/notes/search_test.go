package notes

import "testing"

func TestSearch(t *testing.T) {
	store := NewStore()
	first, _ := store.Add("Buy Milk", nil)
	store.Add("read book", nil)
	store.Add("milk chocolate", nil)

	matched := store.Search("MILK")
	if len(matched) != 2 || matched[0].ID != first.ID {
		t.Fatalf("Search(MILK) = %+v, want two notes starting with %q", matched, first.ID)
	}
	if got := store.Search("zzz"); len(got) != 0 {
		t.Errorf("Search(zzz) = %+v, want no notes", got)
	}
	if got := store.Search(""); len(got) != 0 {
		t.Errorf("Search(empty) = %+v, want no notes", got)
	}
	if got := store.List(); len(got) != 3 {
		t.Errorf("Search modified the store: %d notes", len(got))
	}
}
