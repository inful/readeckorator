// SPDX-License-Identifier: GPL-3.0-or-later

package llm

// ChatRequest is the JSON body posted to /chat/completions.
//
// We always set ResponseFormat to JSON-mode so the model is
// required to emit parseable JSON; that lets the classifier call
// site skip regex scraping.
type ChatRequest struct {
	Model          string    `json:"model"`
	Messages       []Message `json:"messages"`
	Temperature    float64   `json:"temperature,omitempty"`
	MaxTokens      int       `json:"max_tokens,omitempty"`
	ResponseFormat any       `json:"response_format,omitempty"`
}

// Message is one entry in the chat conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse mirrors the subset of the OpenAI chat-completion
// response we actually consume.
type ChatResponse struct {
	ID      string   `json:"id,omitempty"`
	Model   string   `json:"model,omitempty"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Content is a convenience accessor for the first choice's message
// content. Returns "" when there are no choices (Chat already
// rejects that case, but the helper is safe for any caller).
func (r ChatResponse) Content() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// Choice is one assistant reply in the response.
type Choice struct {
	Index   int     `json:"index"`
	Message Message `json:"message"`
}

// Usage reports token counts. Optional — not every endpoint
// populates it.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// JSONMode is the response_format value we always send. Defined as
// a typed value so callers don't have to remember the shape.
var JSONMode = map[string]string{"type": "json_object"}
