package task

import (
	"encoding/json"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a YAML-friendly time.Duration ("10m", "2m30s").
//
// Parsing errors are retained instead of failing YAML decoding so validation
// can report the exact field (for example execution.timeout) and file.
type Duration struct {
	value time.Duration
	raw   string
	err   error
}

// NewDuration wraps a time.Duration.
func NewDuration(value time.Duration) Duration {
	return Duration{value: value}
}

// Duration returns the parsed value.
func (d Duration) Duration() time.Duration { return d.value }

// String renders the duration in Go duration syntax.
func (d Duration) String() string {
	if d.err != nil {
		return d.raw
	}
	return d.value.String()
}

// Err reports a parse error, if any.
func (d Duration) Err() error { return d.err }

// IsZero reports whether no duration was provided.
func (d Duration) IsZero() bool { return d.value == 0 }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		d.raw = value.Value
		d.err = fmt.Errorf("must be a duration string such as \"10m\"")
		return nil
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		d.raw = value.Value
		d.err = fmt.Errorf("invalid duration %q: %w", value.Value, err)
		return nil
	}
	d.value = parsed
	d.raw = value.Value
	d.err = nil
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) {
	if d.err != nil {
		return d.raw, nil
	}
	return d.value.String(), nil
}

// MarshalJSON renders the duration as a string so task fingerprints and JSON
// output include timeout values instead of an empty object.
func (d Duration) MarshalJSON() ([]byte, error) {
	if d.err != nil {
		return json.Marshal(d.raw)
	}
	return json.Marshal(d.value.String())
}
