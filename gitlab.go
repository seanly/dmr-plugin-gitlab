package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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

// MRDiffRefs holds the three SHAs GitLab requires for an inline diff position (API v4, incl. 13.8.x).
type MRDiffRefs struct {
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	StartSHA string `json:"start_sha"`
}

// getMRDiffRefs loads diff_refs from GET /merge_requests/:iid (required for line_code / position validation).
func (c *GitLabClient) getMRDiffRefs(projectID, mrIID int) (MRDiffRefs, error) {
	body, err := c.get(fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d", c.baseURL, projectID, mrIID))
	if err != nil {
		return MRDiffRefs{}, fmt.Errorf("get MR for diff_refs: %w", err)
	}
	var mr struct {
		DiffRefs *MRDiffRefs `json:"diff_refs"`
	}
	if err := json.Unmarshal(body, &mr); err != nil {
		return MRDiffRefs{}, fmt.Errorf("parse MR: %w", err)
	}
	if mr.DiffRefs == nil {
		return MRDiffRefs{}, fmt.Errorf("MR response missing diff_refs (GitLab 13.8+ API v4 expected)")
	}
	r := *mr.DiffRefs
	if r.BaseSHA == "" || r.HeadSHA == "" || r.StartSHA == "" {
		return MRDiffRefs{}, fmt.Errorf("incomplete diff_refs (base_sha/head_sha/start_sha required)")
	}
	return r, nil
}

// hunkHeaderRE matches unified-diff hunk headers from GitLab MR diffs (API v4 / 13.8.x).
var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// fetchAllMRChanges returns every file change for the MR (no ignore/maxLines filter) for position resolution.
func (c *GitLabClient) fetchAllMRChanges(projectID, mrIID int) ([]MRChange, error) {
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
	return result.Changes, nil
}

func findChangeForPath(changes []MRChange, filePath string) *MRChange {
	want := strings.TrimPrefix(filePath, "./")
	for i := range changes {
		ch := &changes[i]
		if ch.NewPath == want || ch.OldPath == want {
			return ch
		}
	}
	return nil
}

// oldLineForNewLineInDiff walks the unified diff for one file and decides whether GitLab needs old_line.
// Pure added lines (+) should omit old_line; context/modified lines need the paired old_line so the UI anchors on "Changes".
func oldLineForNewLineInDiff(diff string, targetNewLine int) (oldLine int, includeOldLine bool, err error) {
	if targetNewLine < 1 {
		return 0, false, fmt.Errorf("invalid target new_line %d", targetNewLine)
	}
	lines := strings.Split(diff, "\n")
	var o, n int
	inHunk := false

	for _, line := range lines {
		if m := hunkHeaderRE.FindStringSubmatch(line); m != nil {
			oldStart, _ := strconv.Atoi(m[1])
			newStart, _ := strconv.Atoi(m[3])
			inHunk = true
			if oldStart == 0 {
				o, n = 0, newStart-1
				if n < 0 {
					n = 0
				}
			} else {
				o, n = oldStart-1, newStart-1
			}
			continue
		}
		if !inHunk {
			continue
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, `\`) { // "\ No newline at end of file"
			continue
		}
		if len(line) < 1 {
			continue
		}
		switch line[0] {
		case ' ':
			o++
			n++
			if n == targetNewLine {
				return o, true, nil
			}
		case '-':
			o++
		case '+':
			n++
			if n == targetNewLine {
				return 0, false, nil
			}
		default:
			// ignore unknown (e.g. corrupted); don't get stuck
		}
	}
	return 0, false, fmt.Errorf("new_line %d not found inside MR unified diff for this file (use a line that appears in the diff)", targetNewLine)
}

// PostDiscussion creates an inline discussion on a specific line of a merge request.
func (c *GitLabClient) PostDiscussion(projectID, mrIID int, filePath string, newLine int, body string) (map[string]any, error) {
	if newLine <= 0 {
		return nil, fmt.Errorf("new_line must be a positive line number in the new file")
	}
	changes, err := c.fetchAllMRChanges(projectID, mrIID)
	if err != nil {
		return nil, err
	}
	ch := findChangeForPath(changes, filePath)
	if ch == nil {
		return nil, fmt.Errorf("file %q not found in MR changes (check path matches new_path from gitlabGetMrDiff)", filePath)
	}

	oldLine, setOldLine, err := oldLineForNewLineInDiff(ch.Diff, newLine)
	if err != nil {
		return nil, err
	}

	refs, err := c.getMRDiffRefs(projectID, mrIID)
	if err != nil {
		return nil, err
	}

	newPath := ch.NewPath
	if newPath == "" {
		newPath = filePath
	}
	oldPath := ch.OldPath

	position := map[string]any{
		"position_type": "text",
		"new_path":      newPath,
		"new_line":      newLine,
		"base_sha":      refs.BaseSHA,
		"head_sha":      refs.HeadSHA,
		"start_sha":     refs.StartSHA,
	}
	if oldPath != "" {
		position["old_path"] = oldPath
	}
	if setOldLine {
		position["old_line"] = oldLine
	}

	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/discussions", c.baseURL, projectID, mrIID)
	payload := map[string]any{
		"body":     body,
		"position": position,
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

// GetProjectDefaultBranch returns the project's default_branch (e.g. main, master).
func (c *GitLabClient) GetProjectDefaultBranch(projectID int) (string, error) {
	apiURL := fmt.Sprintf("%s/api/v4/projects/%d", c.baseURL, projectID)
	body, err := c.get(apiURL)
	if err != nil {
		return "", err
	}
	var proj struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &proj); err != nil {
		return "", fmt.Errorf("parse project: %w", err)
	}
	if strings.TrimSpace(proj.DefaultBranch) == "" {
		return "", fmt.Errorf("project response missing default_branch")
	}
	return proj.DefaultBranch, nil
}

// GetProjectIDByPath resolves a GitLab project id from its URL-encoded path (e.g. "group/sub/repo").
func (c *GitLabClient) GetProjectIDByPath(projectPath string) (int, error) {
	p := strings.TrimSpace(strings.ReplaceAll(projectPath, "\\", "/"))
	p = strings.Trim(p, "/")
	if p == "" {
		return 0, fmt.Errorf("project path required")
	}
	enc := url.PathEscape(p)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s", c.baseURL, enc)
	body, err := c.get(apiURL)
	if err != nil {
		return 0, err
	}
	var proj struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &proj); err != nil {
		return 0, fmt.Errorf("parse project: %w", err)
	}
	if proj.ID == 0 {
		return 0, fmt.Errorf("project response missing id")
	}
	return proj.ID, nil
}

// GetRepositoryFileRaw fetches file contents from the repository ref (branch, tag, or commit SHA).
// filePath is the path in the repo (e.g. ".dmr/review.tpl"); leading slashes are trimmed.
func (c *GitLabClient) GetRepositoryFileRaw(projectID int, filePath, ref string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(filePath, "\\", "/"))
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return "", fmt.Errorf("file_path required")
	}
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("ref required")
	}
	enc := url.PathEscape(p)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%d/repository/files/%s/raw?ref=%s",
		c.baseURL, projectID, enc, url.QueryEscape(ref))
	body, err := c.get(apiURL)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// GetUserByID fetches a GitLab user by id (email visible only with sufficient token permissions).
func (c *GitLabClient) GetUserByID(userID int) (map[string]any, error) {
	url := fmt.Sprintf("%s/api/v4/users/%d", c.baseURL, userID)
	body, err := c.get(url)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse user: %w", err)
	}
	return result, nil
}

// GetMRMeta returns MR summary and author fields for notifications (Feishu, etc.).
// author_email is best-effort: empty if GitLab hides email for the token.
func (c *GitLabClient) GetMRMeta(projectID, mrIID int) (map[string]any, error) {
	info, err := c.GetMRInfo(projectID, mrIID)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{
		"project_id": projectID,
		"mr_iid":     mrIID,
	}
	if t, ok := info["title"].(string); ok {
		meta["title"] = t
	}
	if d, ok := info["description"].(string); ok {
		meta["description"] = d
	}
	if w, ok := info["web_url"].(string); ok {
		meta["web_url"] = w
	}
	if src, ok := info["source_branch"].(string); ok {
		meta["source_branch"] = src
	}
	if tgt, ok := info["target_branch"].(string); ok {
		meta["target_branch"] = tgt
	}
	author, _ := info["author"].(map[string]any)
	var authorID int
	if author != nil {
		if u, ok := author["username"].(string); ok {
			meta["author_username"] = u
		}
		if n, ok := author["name"].(string); ok {
			meta["author_name"] = n
		}
		switch v := author["id"].(type) {
		case float64:
			authorID = int(v)
		case int:
			authorID = v
		case int64:
			authorID = int(v)
		}
		if authorID != 0 {
			meta["author_id"] = authorID
		}
	}
	meta["author_email"] = ""
	if authorID != 0 {
		user, err := c.GetUserByID(authorID)
		if err != nil {
			meta["author_email_error"] = err.Error()
			return meta, nil
		}
		if em, ok := user["email"].(string); ok {
			meta["author_email"] = em
		}
	}
	return meta, nil
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

func (c *GitLabClient) put(url string, payload map[string]any) ([]byte, error) {
	data, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(data)))
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

// UpdateMergeRequest updates MR fields (API v4). Used for work_in_progress / title.
func (c *GitLabClient) UpdateMergeRequest(projectID, mrIID int, fields map[string]any) error {
	url := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d", c.baseURL, projectID, mrIID)
	_, err := c.put(url, fields)
	return err
}

// SetMRWorkInProgress sets GitLab draft/WIP flag (blocks merge when instance policy requires it).
func (c *GitLabClient) SetMRWorkInProgress(projectID, mrIID int, wip bool) error {
	return c.UpdateMergeRequest(projectID, mrIID, map[string]any{"work_in_progress": wip})
}

// GetMRTitle returns the current MR title.
func (c *GitLabClient) GetMRTitle(projectID, mrIID int) (string, error) {
	info, err := c.GetMRInfo(projectID, mrIID)
	if err != nil {
		return "", err
	}
	t, _ := info["title"].(string)
	return t, nil
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
