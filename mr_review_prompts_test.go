package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLookupMRReviewPromptSpec(t *testing.T) {
	doc := &mrReviewPromptsFile{
		Default: "builtin",
		ByPath: map[string]string{
			"g/a":      "file:./x.tmpl",
			"g/b":      "",
			"group/*":  "file:./group-single.tmpl",
			"org/**":   "file:./org-multi.tmpl",
			"exact/match": "file:./exact.tmpl",
		},
	}

	// 精确匹配优先
	spec, ok := lookupMRReviewPromptSpec(doc, "g/a")
	if !ok || spec != "file:./x.tmpl" {
		t.Fatalf("by_path match: got %q ok=%v", spec, ok)
	}
	spec, ok = lookupMRReviewPromptSpec(doc, "  g/a  ")
	if !ok || spec != "file:./x.tmpl" {
		t.Fatalf("trim path: got %q ok=%v", spec, ok)
	}

	// 空值回退到 default
	spec, ok = lookupMRReviewPromptSpec(doc, "g/b")
	if !ok || spec != "builtin" {
		t.Fatalf("empty by_path value should fall through to default: got %q ok=%v", spec, ok)
	}

	// 单层通配符 group/*
	spec, ok = lookupMRReviewPromptSpec(doc, "group/frontend")
	if !ok || spec != "file:./group-single.tmpl" {
		t.Fatalf("single-level wildcard: got %q ok=%v", spec, ok)
	}
	spec, ok = lookupMRReviewPromptSpec(doc, "group/backend")
	if !ok || spec != "file:./group-single.tmpl" {
		t.Fatalf("single-level wildcard: got %q ok=%v", spec, ok)
	}
	// 不匹配多层
	spec, ok = lookupMRReviewPromptSpec(doc, "group/sub/deep")
	if !ok || spec != "builtin" {
		t.Fatalf("single-level wildcard should not match multi-level: got %q ok=%v", spec, ok)
	}

	// 多层通配符 org/**
	spec, ok = lookupMRReviewPromptSpec(doc, "org/project")
	if !ok || spec != "file:./org-multi.tmpl" {
		t.Fatalf("multi-level wildcard: got %q ok=%v", spec, ok)
	}
	spec, ok = lookupMRReviewPromptSpec(doc, "org/sub/deep/project")
	if !ok || spec != "file:./org-multi.tmpl" {
		t.Fatalf("multi-level wildcard deep: got %q ok=%v", spec, ok)
	}

	// 精确匹配优先于通配符
	spec, ok = lookupMRReviewPromptSpec(doc, "exact/match")
	if !ok || spec != "file:./exact.tmpl" {
		t.Fatalf("exact match priority: got %q ok=%v", spec, ok)
	}

	// 未匹配回退到 default
	spec, ok = lookupMRReviewPromptSpec(doc, "unknown")
	if !ok || spec != "builtin" {
		t.Fatalf("default: got %q ok=%v", spec, ok)
	}
	spec, ok = lookupMRReviewPromptSpec(doc, "")
	if !ok || spec != "builtin" {
		t.Fatalf("empty path uses default: got %q ok=%v", spec, ok)
	}

	noDef := &mrReviewPromptsFile{ByPath: map[string]string{"k": "v"}}
	spec, ok = lookupMRReviewPromptSpec(noDef, "missing")
	if ok {
		t.Fatalf("no default and no match: expected !ok, got %q", spec)
	}
}

func TestLoadMRReviewPrompts_reloadOnMtime(t *testing.T) {
	base := t.TempDir()
	jsonPath := filepath.Join(base, "mr.json")
	if err := os.WriteFile(jsonPath, []byte(`{"default":"inline-a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &WebhookServer{
		config: GitLabPluginConfig{
			ConfigBaseDir:       base,
			MRPromptsFile: "mr.json",
		},
	}
	doc, err := s.loadMRReviewPrompts()
	if err != nil {
		t.Fatal(err)
	}
	if doc.Default != "inline-a" {
		t.Fatalf("first load: %#v", doc)
	}
	// same mtime: cached
	doc2, err := s.loadMRReviewPrompts()
	if err != nil {
		t.Fatal(err)
	}
	if doc2.Default != "inline-a" {
		t.Fatal("cache")
	}
	// change file
	if err := os.WriteFile(jsonPath, []byte(`{"default":"inline-b"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc3, err := s.loadMRReviewPrompts()
	if err != nil {
		t.Fatal(err)
	}
	if doc3.Default != "inline-b" {
		t.Fatalf("reload: got %#v", doc3)
	}
}

func TestMRPromptsJSON_unmarshal(t *testing.T) {
	raw := `{
  "default": "builtin",
  "by_path": { "group/sub/frontend": "file:./prompts/f.tmpl" }
}`
	var doc mrReviewPromptsFile
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Default != "builtin" || doc.ByPath["group/sub/frontend"] != "file:./prompts/f.tmpl" {
		t.Fatalf("%+v", doc)
	}
}
