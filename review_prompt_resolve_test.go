package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveReviewTemplate_noMappingFileUsesBuiltin(t *testing.T) {
	s := &WebhookServer{config: GitLabPluginConfig{}}
	if got := s.resolveReviewTemplate("g/r"); got != DefaultReviewPrompt {
		t.Fatalf("expected builtin default")
	}
}

func TestResolveReviewTemplate_jsonNoMatchUsesBuiltin(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "m.json"), []byte(`{"by_path":{"other/x": "builtin"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &WebhookServer{config: GitLabPluginConfig{
		ConfigBaseDir:       base,
		MRPromptsFile: "m.json",
	}}
	if got := s.resolveReviewTemplate("g/r"); got != DefaultReviewPrompt {
		t.Fatalf("no match and no default → builtin")
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
