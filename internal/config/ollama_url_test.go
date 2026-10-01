package config

import "testing"

// The address is typed by hand. A capitalised scheme or a pasted OpenAI-style
// /v1 suffix must not make the backend call /v1/v1/chat/completions or reject
// an address that is fine.
func TestNormalizeOllamaURLAcceptsHandTypedForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"HTTP://Localhost:11434", "http://Localhost:11434"},
		{"HTTPS://box.lan", "https://box.lan"},
		{"http://localhost:11434/v1", "http://localhost:11434"},
		{"http://localhost:11434/v1/", "http://localhost:11434"},
		{"localhost:11434/V1", "http://localhost:11434"},
		{"http://localhost:11434//", "http://localhost:11434"},
		{"ftp://localhost", Defaults().OllamaURL},
		{"", Defaults().OllamaURL},
	}
	for _, c := range cases {
		if got := NormalizeOllamaURL(c.in); got != c.want {
			t.Errorf("NormalizeOllamaURL(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// A host that happens to be called v1 is a host, not the API path, and must
// survive normalising.
func TestNormalizeOllamaURLKeepsAHostNamedV1(t *testing.T) {
	if got, want := NormalizeOllamaURL("http://v1"), "http://v1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
