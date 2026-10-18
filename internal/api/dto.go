// Package api holds the HTTP routes and wire types.
package api

// AskRequest is the POST /ask body.
type AskRequest struct {
	Question string `json:"question"`
}

// AskResponse is the POST /ask success body — exactly these two keys.
type AskResponse struct {
	Answer  string   `json:"answer"`
	Sources []string `json:"sources"`
}

// ErrorResponse is every error body.
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse is the GET /healthz body.
type HealthResponse struct {
	Status       string `json:"status"`
	IndexVersion string `json:"index_version"`
	Chunks       int    `json:"chunks"`
	Vectors      bool   `json:"vectors"`
	LLM          bool   `json:"llm"`
}

// VersionResponse is GET /version.
type VersionResponse struct {
	Commit     string `json:"commit"`
	Deployment string `json:"deployment"`
}

// ModelsResponse is GET /models.
type ModelsResponse struct {
	Models []string `json:"models"`
}
