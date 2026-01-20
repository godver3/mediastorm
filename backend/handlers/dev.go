package handlers

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// DevHandler handles developer-only endpoints (localhost restricted)
type DevHandler struct {
	docsPath string
}

// NewDevHandler creates a new dev handler
func NewDevHandler(docsPath string) *DevHandler {
	return &DevHandler{docsPath: docsPath}
}

// TodoEditor serves a simple HTML editor for TODO.md
func (h *DevHandler) TodoEditor(w http.ResponseWriter, r *http.Request) {
	todoPath := filepath.Join(h.docsPath, "TODO.md")

	if r.Method == http.MethodGet {
		content, err := os.ReadFile(todoPath)
		if err != nil {
			http.Error(w, "Failed to read TODO.md: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
	<title>TODO Editor</title>
	<style>
		* { box-sizing: border-box; }
		html, body { height: 100%; margin: 0; overflow: hidden; }
		body {
			font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
			padding: 20px; background: #1a1a2e; color: #eee;
			display: flex; flex-direction: column;
		}
		.header { display: flex; align-items: center; gap: 15px; margin-bottom: 15px; flex-shrink: 0; }
		h1 { margin: 0; font-size: 1.5em; color: #fff; }
		button {
			padding: 10px 24px; font-size: 14px; font-weight: 500;
			border: none; border-radius: 6px; cursor: pointer;
			background: #0f4c75; color: #fff;
		}
		button:hover { background: #1b6ca8; }
		.status { color: #888; font-size: 13px; }
		.status.success { color: #4caf50; }
		.status.error { color: #f44336; }
		textarea {
			flex: 1; width: 100%;
			font-family: 'Monaco', 'Menlo', 'Consolas', monospace;
			font-size: 14px; line-height: 1.5;
			padding: 15px; border: 1px solid #333; border-radius: 8px;
			background: #16213e; color: #eee; resize: none;
		}
		textarea:focus { outline: none; border-color: #0f4c75; }
	</style>
</head>
<body>
	<div class="header">
		<h1>TODO.md</h1>
		<button onclick="save()">Save</button>
		<span id="status" class="status"></span>
	</div>
	<textarea id="content">` + escapeHTML(string(content)) + `</textarea>
	<script>
		function save() {
			const status = document.getElementById('status');
			status.textContent = 'Saving...';
			status.className = 'status';

			fetch(window.location.href, {
				method: 'POST',
				headers: { 'Content-Type': 'text/plain' },
				body: document.getElementById('content').value
			})
			.then(r => {
				if (r.ok) {
					status.textContent = 'Saved!';
					status.className = 'status success';
				} else {
					r.text().then(t => {
						status.textContent = 'Error: ' + t;
						status.className = 'status error';
					});
				}
			})
			.catch(e => {
				status.textContent = 'Error: ' + e.message;
				status.className = 'status error';
			});
		}

		document.getElementById('content').addEventListener('keydown', function(e) {
			if ((e.ctrlKey || e.metaKey) && e.key === 's') {
				e.preventDefault();
				save();
			}
		});
	</script>
</body>
</html>`))
		return
	}

	if r.Method == http.MethodPost {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		if err := os.WriteFile(todoPath, body, 0644); err != nil {
			http.Error(w, "Failed to write TODO.md: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func escapeHTML(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			result = append(result, []byte("&lt;")...)
		case '>':
			result = append(result, []byte("&gt;")...)
		case '&':
			result = append(result, []byte("&amp;")...)
		case '"':
			result = append(result, []byte("&quot;")...)
		default:
			result = append(result, s[i])
		}
	}
	return string(result)
}
