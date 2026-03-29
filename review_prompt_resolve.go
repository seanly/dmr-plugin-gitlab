package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// resolveReviewTemplate returns the Go text/template body before MR field substitution.
// Uses mr_prompts_file when set (by_path then default); otherwise DefaultReviewPrompt.
// Load or lookup errors fall back to builtin with a log line.
func (s *WebhookServer) resolveReviewTemplate(pathWithNamespace string) string {
	if strings.TrimSpace(s.config.MRPromptsFile) != "" {
		doc, err := s.loadMRReviewPrompts()
		if err != nil {
			log.Printf("dmr-plugin-gitlab: mr_prompts_file: %v (using builtin)", err)
			return DefaultReviewPrompt
		}
		if spec, ok := lookupMRReviewPromptSpec(doc, pathWithNamespace); ok {
			log.Printf("dmr-plugin-gitlab: review prompt from mr_prompts_file (path=%q)", strings.TrimSpace(pathWithNamespace))
			return s.resolveReviewPromptFromSpecifier(spec)
		}
		log.Printf("dmr-plugin-gitlab: mr_prompts_file: no matching by_path or default (path=%q), using builtin", strings.TrimSpace(pathWithNamespace))
	}
	return DefaultReviewPrompt
}

// resolveReviewPromptFromSpecifier resolves builtin | file: | relative path | absolute path | inline template.
func (s *WebhookServer) resolveReviewPromptFromSpecifier(rp string) string {
	rp = strings.TrimSpace(rp)
	if rp == "" {
		log.Printf("dmr-plugin-gitlab: empty review prompt specifier, using builtin")
		return DefaultReviewPrompt
	}
	if strings.EqualFold(rp, "builtin") {
		log.Printf("dmr-plugin-gitlab: review prompt specifier=builtin")
		return DefaultReviewPrompt
	}

	var pathCandidate string
	switch {
	case strings.HasPrefix(rp, "file:"):
		pathCandidate = strings.TrimSpace(strings.TrimPrefix(rp, "file:"))
	case strings.HasPrefix(rp, "./") || strings.HasPrefix(rp, "../"):
		pathCandidate = rp
	default:
		if filepath.IsAbs(rp) {
			pathCandidate = rp
		}
	}

	if pathCandidate != "" {
		abs, err := resolveLocalPromptPath(pathCandidate, s.config.ConfigBaseDir)
		if err != nil {
			log.Printf("dmr-plugin-gitlab: review prompt file %q: %v, using builtin", pathCandidate, err)
			return DefaultReviewPrompt
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			log.Printf("dmr-plugin-gitlab: read review prompt file %s: %v, using builtin", abs, err)
			return DefaultReviewPrompt
		}
		log.Printf("dmr-plugin-gitlab: review prompt from local file %s (%d bytes)", abs, len(b))
		return string(b)
	}

	return rp
}

// resolveLocalPromptPath turns user path into an absolute path; relative paths are under config_base_dir.
func resolveLocalPromptPath(userPath, configBaseDir string) (string, error) {
	userPath = strings.TrimSpace(userPath)
	if userPath == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(userPath) {
		return filepath.Clean(userPath), nil
	}
	base := strings.TrimSpace(configBaseDir)
	if base == "" {
		return "", errConfigBaseDirMissing
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	rel := filepath.Clean(filepath.FromSlash(filepath.ToSlash(userPath)))
	abs := filepath.Join(baseAbs, rel)
	abs = filepath.Clean(abs)
	relOut, err := filepath.Rel(baseAbs, abs)
	if err != nil || strings.HasPrefix(relOut, "..") {
		return "", errPathEscapesBase
	}
	return abs, nil
}

var (
	errConfigBaseDirMissing = &promptPathError{"config_base_dir required for relative path"}
	errPathEscapesBase      = &promptPathError{"path escapes config_base_dir"}
)

type promptPathError struct{ msg string }

func (e *promptPathError) Error() string { return e.msg }
