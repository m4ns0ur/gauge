package text

import "testing"

func TestSnakeCase(t *testing.T) {
	cases := map[string]string{
		"HelloWorld":   "hello_world",
		"hello world":  "hello_world",
		"Hello-World":  "hello_world",
		"already_snake": "already_snake",
		"HTTPServer":   "http_server",
		"userID":       "user_id",
		"HTTP":         "http",
		"two  spaces":  "two_spaces",
		"":             "",
	}
	for input, want := range cases {
		if got := SnakeCase(input); got != want {
			t.Errorf("SnakeCase(%q) = %q, want %q", input, got, want)
		}
	}
}
