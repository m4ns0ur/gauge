// Package notes provides an in-memory note store.
package notes

import (
	"errors"
	"fmt"
)

// Note is one stored note.
type Note struct {
	ID   string
	Text string
	Tags []string
}

// Store is an in-memory collection of notes.
type Store struct {
	notes []Note
	next  int
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{} }

// Add stores a note with a generated ID.
func (s *Store) Add(text string, tags []string) (Note, error) {
	if text == "" {
		return Note{}, errors.New("notes: text is required")
	}
	s.next++
	note := Note{ID: fmt.Sprintf("n-%d", s.next), Text: text, Tags: tags}
	s.notes = append(s.notes, note)
	return note, nil
}

// List returns the stored notes in insertion order.
func (s *Store) List() []Note {
	return append([]Note(nil), s.notes...)
}

// Find returns the note with the given ID.
func (s *Store) Find(id string) (Note, bool) {
	for _, note := range s.notes {
		if note.ID == id {
			return note, true
		}
	}
	return Note{}, false
}

// Delete removes the note with the given ID.
//
// TODO: not implemented yet; the current version does nothing.
func (s *Store) Delete(id string) error {
	return nil
}

// FilterByTag returns the notes that carry tag, preserving store order.
//
// TODO: not implemented yet; the current version returns every note.
func (s *Store) FilterByTag(tag string) []Note {
	return s.List()
}
