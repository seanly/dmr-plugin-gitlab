package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// mrReviewPromptsFile matches mr-review-prompts.json (by_path + default only).
type mrReviewPromptsFile struct {
	Default string            `json:"default"`
	ByPath  map[string]string `json:"by_path"`
}

var errMRReviewPromptsNotConfigured = errors.New("mr_prompts_file not set")

func lookupMRReviewPromptSpec(doc *mrReviewPromptsFile, pathWithNamespace string) (spec string, ok bool) {
	if doc == nil {
		return "", false
	}
	p := strings.TrimSpace(pathWithNamespace)
	if p != "" && doc.ByPath != nil {
		// 1. 精确匹配优先
		if v, exists := doc.ByPath[p]; exists {
			v = strings.TrimSpace(v)
			if v != "" {
				return v, true
			}
		}
		// 2. 通配符匹配（支持 group/* 和 group/**）
		for pattern, v := range doc.ByPath {
			if matchPathPattern(pattern, p) {
				v = strings.TrimSpace(v)
				if v != "" {
					return v, true
				}
			}
		}
	}
	d := strings.TrimSpace(doc.Default)
	if d != "" {
		return d, true
	}
	return "", false
}

// matchPathPattern checks if path matches a glob-like pattern.
// Supports:
//   - exact match: "group/project"
//   - single-level wildcard: "group/*" matches "group/foo" but not "group/sub/foo"
//   - multi-level wildcard: "group/**" matches "group/foo" and "group/sub/foo"
func matchPathPattern(pattern, path string) bool {
	pattern = strings.TrimSpace(pattern)
	path = strings.TrimSpace(path)

	// 精确匹配
	if pattern == path {
		return true
	}

	// 单层通配符 group/*
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		if !strings.HasPrefix(path, prefix+"/") {
			return false
		}
		remainder := strings.TrimPrefix(path, prefix+"/")
		// 不能包含更多的 /
		return !strings.Contains(remainder, "/")
	}

	// 多层通配符 group/**
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return strings.HasPrefix(path, prefix+"/")
	}

	return false
}

func (s *WebhookServer) resolvedMRReviewPromptsPath() (string, error) {
	p := strings.TrimSpace(s.config.MRPromptsFile)
	if p == "" {
		return "", errMRReviewPromptsNotConfigured
	}
	return resolveLocalPromptPath(p, s.config.ConfigBaseDir)
}

// loadMRReviewPrompts reads and caches mr-review-prompts.json; reloads when mtime changes.
func (s *WebhookServer) loadMRReviewPrompts() (*mrReviewPromptsFile, error) {
	path, err := s.resolvedMRReviewPromptsPath()
	if err != nil {
		return nil, err
	}

	s.mrReviewMu.Lock()
	defer s.mrReviewMu.Unlock()

	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if s.mrReviewDoc != nil && s.mrReviewPath == path && !fi.ModTime().After(s.mrReviewMod) {
		return s.mrReviewDoc, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc mrReviewPromptsFile
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	s.mrReviewPath = path
	s.mrReviewMod = fi.ModTime()
	s.mrReviewDoc = &doc
	log.Printf("dmr-plugin-gitlab: loaded mr_prompts_file %s (mtime %s)", path, fi.ModTime().Format(time.RFC3339))
	return s.mrReviewDoc, nil
}
