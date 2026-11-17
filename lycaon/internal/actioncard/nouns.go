package actioncard

// nouns names what each tool's Ran text is; tools not listed name a target.
var nouns = map[string]string{
	"command": "command", "verify": "command", "command_output": "command", "command_stop": "command",
	"terminal_open": "command", "terminal_send": "command", "terminal_read": "command", "terminal_snapshot": "command", "terminal_close": "command",
	"read": "path", "held_result": "path", "find": "pattern", "grep": "pattern", "list_dir": "path", "stat": "path", "wc": "path",
	"jq": "query", "diff": "path", "recall": "query", "git_blame": "path", "source_history": "path",
	"survey_repo": "path", "summarize": "path", "write": "path", "edit": "path", "replace_lines": "path", "code_rewrite": "pattern",
	"jq_edit": "path", "move": "path", "copy": "path", "delete": "path", "mkdir": "path", "chmod": "path", "chown": "path",
	"extract_archive": "path", "restore_version": "path",
	"fetch_url": "url", "http_request": "request", "web_search": "query",
	"page_open": "url", "page_act": "target", "page_snapshot": "target", "capture_page": "url", "measure_page": "url", "page_close": "target",
	"view_image": "path", "view_video": "path", "render_view": "target",
	"git_commit": "message", "git_merge": "message", "git_stash": "message",
	"workflow_advance": "transition", "workflow_transition": "transition",
	"skills_read": "skill", "request_tools": "need", "submit_verdict": "phase",
	"scan_summary": "scan", "scan_query": "scan", "scan_compare": "scan", "scan_list": "scan", "scan_pack": "scan",
}

func nounFor(tool string) string {
	if noun, ok := nouns[tool]; ok {
		return noun
	}
	return "target"
}
