package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func normalizeReviewPromptSource(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "builtin", "config", "current", "external":
		return s
	default:
		if s != "" {
			log.Printf("dmr-plugin-gitlab: unknown review_prompt_source %q, using builtin", raw)
		}
		return "builtin"
	}
}

// effectiveReviewPromptSource applies legacy rules when review_prompt_source is unset:
// review_prompt_file → current; review_prompt → config; else builtin.
func (s *WebhookServer) effectiveReviewPromptSource() string {
	raw := strings.TrimSpace(s.config.ReviewPromptSource)
	if raw == "" {
		if strings.TrimSpace(s.config.ReviewPromptFile) != "" {
			return "current"
		}
		if strings.TrimSpace(s.config.ReviewPrompt) != "" {
			return "config"
		}
		return "builtin"
	}
	return normalizeReviewPromptSource(raw)
}

// resolveReviewTemplate returns the Go text/template body before MR field substitution.
func (s *WebhookServer) resolveReviewTemplate(projectID int) string {
	src := s.effectiveReviewPromptSource()
	switch src {
	case "builtin":
		log.Printf("dmr-plugin-gitlab: review prompt source=builtin")
		return DefaultReviewPrompt
	case "config":
		return s.resolveConfigReviewPrompt()
	case "current":
		return s.resolveCurrentRepoReviewPrompt(projectID)
	case "external":
		return s.resolveExternalRepoReviewPrompt()
	default:
		return DefaultReviewPrompt
	}
}

func (s *WebhookServer) resolveConfigReviewPrompt() string {
	rp := strings.TrimSpace(s.config.ReviewPrompt)
	if rp == "" {
		log.Printf("dmr-plugin-gitlab: review_prompt_source=config but review_prompt empty, using builtin")
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
			log.Printf("dmr-plugin-gitlab: config review_prompt file %q: %v, using builtin", pathCandidate, err)
			return DefaultReviewPrompt
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			log.Printf("dmr-plugin-gitlab: read review_prompt file %s: %v, using builtin", abs, err)
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
	errConfigBaseDirMissing = &promptPathError{"config_base_dir required for relative review_prompt path"}
	errPathEscapesBase      = &promptPathError{"review_prompt path escapes config_base_dir"}
)

type promptPathError struct{ msg string }

func (e *promptPathError) Error() string { return e.msg }

func (s *WebhookServer) resolveCurrentRepoReviewPrompt(projectID int) string {
	file := strings.TrimSpace(s.config.ReviewPromptFile)
	if file == "" {
		log.Printf("dmr-plugin-gitlab: review_prompt_source=current but review_prompt_file empty, using builtin")
		return DefaultReviewPrompt
	}
	ref := strings.TrimSpace(s.config.ReviewPromptRef)
	if ref == "" {
		db, err := s.glClient.GetProjectDefaultBranch(projectID)
		if err != nil {
			log.Printf("dmr-plugin-gitlab: current: default_branch: %v, using builtin", err)
			return DefaultReviewPrompt
		}
		ref = db
	}
	content, err := s.glClient.GetRepositoryFileRaw(projectID, file, ref)
	if err != nil {
		log.Printf("dmr-plugin-gitlab: current: file %q @ %s: %v", file, ref, err)
		return s.fallbackInlineOrBuiltin()
	}
	log.Printf("dmr-plugin-gitlab: review prompt from current project file %q @ %s (%d bytes)", file, ref, len(content))
	return content
}

func (s *WebhookServer) resolveExternalRepoReviewPrompt() string {
	projPath := strings.TrimSpace(s.config.ReviewPromptProjectPath)
	file := strings.TrimSpace(s.config.ReviewPromptFile)
	if projPath == "" || file == "" {
		log.Printf("dmr-plugin-gitlab: review_prompt_source=external requires review_prompt_project_path and review_prompt_file, using builtin")
		return DefaultReviewPrompt
	}
	extID, err := s.glClient.GetProjectIDByPath(projPath)
	if err != nil {
		log.Printf("dmr-plugin-gitlab: external: resolve project %q: %v", projPath, err)
		return s.fallbackInlineOrBuiltin()
	}
	ref := strings.TrimSpace(s.config.ReviewPromptRef)
	if ref == "" {
		db, err := s.glClient.GetProjectDefaultBranch(extID)
		if err != nil {
			log.Printf("dmr-plugin-gitlab: external: default_branch: %v", err)
			return s.fallbackInlineOrBuiltin()
		}
		ref = db
	}
	content, err := s.glClient.GetRepositoryFileRaw(extID, file, ref)
	if err != nil {
		log.Printf("dmr-plugin-gitlab: external: file %q in %q @ %s: %v", file, projPath, ref, err)
		return s.fallbackInlineOrBuiltin()
	}
	log.Printf("dmr-plugin-gitlab: review prompt from external project %q file %q @ %s (%d bytes)", projPath, file, ref, len(content))
	return content
}

func (s *WebhookServer) fallbackInlineOrBuiltin() string {
	inline := strings.TrimSpace(s.config.ReviewPrompt)
	if inline == "" {
		return DefaultReviewPrompt
	}
	log.Printf("dmr-plugin-gitlab: falling back to inline review_prompt (%d bytes)", len(inline))
	return inline
}
