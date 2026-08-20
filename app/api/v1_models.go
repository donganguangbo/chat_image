package api

import (
	"net/http"
	"strings"
	"time"
)

// V1Model is one model in the OpenAI-standard GET /v1/models response body.
// Many OpenAI-shaped local clients (Chatbox, new-api, etc.) probe this endpoint
// before connecting; without it they see Go's mux "404 page not found" and
// refuse to connect.
type V1Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type V1ModelList struct {
	Object string    `json:"object"`
	Data   []V1Model `json:"data"`
}

// HandleListModels implements GET /v1/models. It advertises the text model when
// a text backend is configured, plus OpenAI-shaped aliases for each image/video
// category that has a default workflow, so clients can discover and select what
// this gateway can actually serve.
func (h *V1Handler) HandleListModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	now := time.Now().Unix()
	data := make([]V1Model, 0, 8)

	if strings.TrimSpace(h.service.TextBaseURL()) != "" {
		id := strings.TrimSpace(h.service.TextModelName())
		if id == "" {
			id = "qwen-local"
		}
		data = append(data, V1Model{ID: id, Object: "model", Created: now, OwnedBy: "chat_image"})
	}

	seen := map[string]bool{}
	aliases := map[string][]string{
		"t2i":  {"gpt-image-1", "dall-e-3"},
		"edit": {"gpt-image-edit-1", "image-edit"},
		"t2v":  {"sora-1", "text-to-video"},
		"i2v":  {"video", "image-to-video"},
	}
	for _, cat := range []string{"t2i", "edit", "t2v", "i2v"} {
		if h.service.WorkflowForCategory(cat) == "" {
			continue
		}
		for _, alias := range aliases[cat] {
			if seen[alias] {
				continue
			}
			seen[alias] = true
			data = append(data, V1Model{ID: alias, Object: "model", Created: now, OwnedBy: "chat_image"})
		}
	}

	writeJSON(w, http.StatusOK, V1ModelList{Object: "list", Data: data})
}
