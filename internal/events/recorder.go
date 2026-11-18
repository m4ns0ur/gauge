package events

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Recorder serializes normalized events as newline-delimited JSON.
//
// A Recorder is safe for concurrent use, which matters because adapters may
// stream events from several goroutines. Emit never returns an error: it
// records the first write failure and reports it from Close, so the adapter
// callback signature stays small and adapters cannot fail a run because log
// collection had a problem.
type Recorder struct {
	mu     sync.Mutex
	enc    *json.Encoder
	closer io.Closer
	source string
	seq    int
	err    error
	now    func() time.Time
}

// NewRecorder writes JSONL events to w and stamps events from source.
func NewRecorder(w io.WriteCloser, source string) *Recorder {
	return &Recorder{
		enc:    json.NewEncoder(w),
		closer: w,
		source: source,
		now:    time.Now,
	}
}

// Emit fills in the envelope fields the caller left unset and writes the event.
func (r *Recorder) Emit(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	r.seq++
	if ev.SchemaVersion == 0 {
		ev.SchemaVersion = SchemaVersion
	}
	if ev.Seq == 0 {
		ev.Seq = r.seq
	}
	if ev.Time.IsZero() {
		ev.Time = r.now().UTC()
	}
	if ev.Source == "" {
		ev.Source = r.source
	}
	if !ev.Type.Valid() {
		r.err = fmt.Errorf("events: invalid event type %q", ev.Type)
		return
	}
	if err := r.enc.Encode(ev); err != nil {
		r.err = fmt.Errorf("events: write event: %w", err)
	}
}

// Err reports the first error encountered while recording.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Close flushes the underlying writer. It returns any recording error.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closer != nil {
		if err := r.closer.Close(); err != nil && r.err == nil {
			r.err = fmt.Errorf("events: close recorder: %w", err)
		}
		r.closer = nil
	}
	return r.err
}
