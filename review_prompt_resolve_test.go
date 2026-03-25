package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeReviewPromptSource(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "builtin"},
		{"  Builtin  ", "builtin"},
		{"CONFIG", "config"},
		{"current", "current"},
		{"external", "external"},
		{"garbage", "builtin"},
	}
	for _, tc := range tests {
		got := normalizeReviewPromptSource(tc.in)
		if got != tc.want {
			t.Errorf("normalizeReviewPromptSource(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveLocalPromptPath_relative_okAndEscape(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveLocalPromptPath("./sub/x.md", base)
	if err != nil {
		t.Fatalf("relative ok: %v", err)
	}
	want := filepath.Join(base, "sub", "x.md")
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("got %q want %q", got, want)
	}

	_, err = resolveLocalPromptPath("../outside", filepath.Join(base, "sub"))
	if err == nil {
		t.Fatal("expected error for path escape")
	}
}

func TestResolveLocalPromptPath_abs(t *testing.T) {
	base := t.TempDir()
	abs := filepath.Join(base, "abs.md")
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveLocalPromptPath(abs, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) != filepath.Clean(abs) {
		t.Fatalf("got %q want %q", got, abs)
	}
}
