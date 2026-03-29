package main

import (
	"encoding/json"
	"testing"
)

func TestAuthorEmailFromWebhook(t *testing.T) {
	const payload = `{
  "object_kind": "merge_request",
  "user": { "id": 1, "email": "actor@example.com" },
  "project": { "id": 1, "name": "p", "path_with_namespace": "g/p" },
  "object_attributes": {
    "iid": 2,
    "title": "t",
    "description": "",
    "action": "open",
    "state": "opened",
    "source_branch": "a",
    "target_branch": "b",
    "last_commit": {
      "id": "abc",
      "author": { "name": "Alice", "email": "alice@example.com" }
    }
  }
}`
	var ev GitLabWebhookEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatal(err)
	}
	if got := authorEmailFromWebhook(ev); got != "alice@example.com" {
		t.Fatalf("last_commit.author.email should win: got %q", got)
	}

	ev.ObjectAttributes.LastCommit = nil
	if got := authorEmailFromWebhook(ev); got != "actor@example.com" {
		t.Fatalf("user.email fallback: got %q", got)
	}
}
