package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GitLabClient wraps GitLab REST API calls.
type GitLabClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewGitLabClient creates a new GitLab API client.
func NewGitLabClient(baseURL, token string) *GitLabClient {
	return &GitLabClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{},
	}
}

// MRChange represents a single file change in a merge request.
type MRChange struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

// GetMRDiff fetches the diff of a merge request, filtering by maxLines and ignore patterns.
func (c *GitLabClient) GetMRDiff(projectID, mrIID, maxLines int, ignorePatterns []string) ([]MRChange, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/changes", c.baseURL, projectID, mrIID)
	body, err := c.get(url)
	if err != nil {
		return nil, fmt.Errorf("get MR changes: %w", err)
	}

	var result struct {
		Changes []MRChange `json:"changes"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse MR changes: %w", err)
	}

	var filtered []MRChange
	for _, ch := range result.Changes {
		if shouldIgnore(ch.NewPath, ignorePatterns) {
			continue
		}
		if maxLines > 0 && countLines(ch.Diff) > maxLines {
			continue
		}
		filtered = append(filtered, ch)
	}
	return filtered, nil
}

// PostComment creates a note (comment) on a merge request.
func (c *GitLabClient) PostComment(projectID, mrIID int, body string) (map[string]any, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/notes", c.baseURL, projectID, mrIID)
	payload := map[string]any{"body": body}
	respBody, err := c.post(url, payload)
	if err != nil {
		return nil, fmt.Errorf("post comment: %w", err)
	}
	var result map[string]any
	json.Unmarshal(respBody, &result)
	return result, nil
}

// PostDiscussion creates an inline discussion on a specific line of a merge request.
func (c *GitLabClient) PostDiscussion(projectID, mrIID int, filePath string, newLine int, body string) (map[string]any, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/discussions", c.baseURL, projectID, mrIID)
	payload := map[string]any{
		"body": body,
		"position": map[string]any{
			"position_type": "text",
			"new_path":      filePath,
			"new_line":      newLine,
			"base_sha":      "",
			"head_sha":      "",
			"start_sha":     "",
		},
	}
	respBody, err := c.post(url, payload)
	if err != nil {
		return nil, fmt.Errorf("post discussion: %w", err)
	}
	var result map[string]any
	json.Unmarshal(respBody, &result)
	return result, nil
}

// GetMRInfo fetches basic merge request info (for SHA values needed by discussions).
func (c *GitLabClient) GetMRInfo(projectID, mrIID int) (map[string]any, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d", c.baseURL, projectID, mrIID)
	body, err := c.get(url)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	json.Unmarshal(body, &result)
	return result, nil
}

// --- HTTP helpers ---

func (c *GitLabClient) get(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitLab API %s: %d %s", url, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *GitLabClient) post(url string, payload map[string]any) ([]byte, error) {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitLab API %s: %d %s", url, resp.StatusCode, string(body))
	}
	return body, nil
}

// --- Utility ---

func shouldIgnore(path string, patterns []string) bool {
	for _, p := range patterns {
		if matched, _ := matchGlob(p, path); matched {
			return true
		}
	}
	return false
}

func matchGlob(pattern, name string) (bool, error) {
	// Simple glob: support * and **
	if pattern == "**" {
		return true, nil
	}
	if strings.HasPrefix(pattern, "*.") {
		ext := pattern[1:] // e.g. ".lock"
		return strings.HasSuffix(name, ext), nil
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := pattern[:len(pattern)-3]
		return strings.HasPrefix(name, prefix+"/") || name == prefix, nil
	}
	return name == pattern, nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
