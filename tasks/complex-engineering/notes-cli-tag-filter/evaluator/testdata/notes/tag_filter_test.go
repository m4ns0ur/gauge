package notes

import "testing"

func TestFilterByTag(t *testing.T) {
	store := NewStore()
	one, _ := store.Add("one", []string{"work", "urgent"})
	two, _ := store.Add("two", []string{"home"})
	three, _ := store.Add("three", []string{"WORK"})

	matched := store.FilterByTag("work")
	if len(matched) != 2 || matched[0].ID != one.ID || matched[1].ID != three.ID {
		t.Fatalf("FilterByTag(work) = %+v, want %q then %q", matched, one.ID, three.ID)
	}
	if got := store.FilterByTag("nope"); len(got) != 0 {
		t.Errorf("FilterByTag(nope) = %+v, want no notes", got)
	}
	if got := store.FilterByTag(""); len(got) != 0 {
		t.Errorf("FilterByTag(empty) = %+v, want no notes", got)
	}
	if got := store.List(); len(got) != 3 {
		t.Errorf("FilterByTag modified the store: %d notes", len(got))
	}
	_ = two
}
