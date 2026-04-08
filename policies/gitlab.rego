package dmr

# GitLab plugin policy

# Allow: Read-only operations
decision = {"action": "allow", "reason": "gitlab read-only query", "risk": "low"} if {
	input.tool in [
		"gitlabGetMrDiff",
		"gitlabGetMrMeta"
	]
}

# Require approval: Write operations
decision = {"action": "require_approval", "reason": "gitlab write: post comment", "risk": "low"} if {
	input.tool == "gitlabPostComment"
}

decision = {"action": "require_approval", "reason": "gitlab write: post discussion", "risk": "low"} if {
	input.tool == "gitlabPostDiscussion"
}

decision = {"action": "require_approval", "reason": "gitlab write: add webhook", "risk": "high"} if {
	input.tool == "gitlabAddWebhook"
}
