package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"olake-ask/internal/domain"
	"olake-ask/internal/middleware"
	"olake-ask/internal/tuning"
	"olake-ask/web"
)

// Options configures the router.
type Options struct {
	Answerer       domain.Answerer
	Health         HealthResponse // Status is filled in
	AllowedOrigins []string
	RateLimiter    *middleware.RateLimiter
	Logger         *slog.Logger
}

// NewRouter builds the Gin engine with all routes.
func NewRouter(o Options) (*gin.Engine, error) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}
	r.Use(middleware.RequestID(), middleware.Recover(), middleware.Logger(logger), middleware.CORS(middleware.NewOriginMatcher(o.AllowedOrigins)))

	h := &handler{answerer: o.Answerer, health: o.Health}
	h.health.Status = "ok"

	r.POST("/ask", middleware.BodyLimit(tuning.MaxBodyBytes), o.RateLimiter.Middleware(), h.ask)
	r.OPTIONS("/ask", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.GET("/healthz", h.healthz)
	r.HEAD("/healthz", h.healthz)
	r.GET("/", h.root)
	r.HEAD("/", h.root)
	r.GET("/widget.js", widget)
	r.GET("/ping", ping)
	r.HEAD("/ping", ping)
	r.NoRoute(func(c *gin.Context) { c.JSON(http.StatusNotFound, ErrorResponse{Error: "not found"}) })
	return r, nil
}

type handler struct {
	answerer domain.Answerer
	health   HealthResponse
}

func (h *handler) ask(c *gin.Context) {
	var req AskRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		if middleware.IsTooLarge(err) {
			c.JSON(http.StatusRequestEntityTooLarge, ErrorResponse{Error: "request too large"})
			return
		}
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid JSON body"})
		return
	}
	q := strings.TrimSpace(req.Question)
	if q == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "question is required"})
		return
	}
	if utf8.RuneCountInString(q) > tuning.MaxQuestionChars {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "question too long (max 2000 characters)"})
		return
	}
	c.Set(middleware.KeyQuestion, q)

	ctx, cancel := context.WithTimeout(c.Request.Context(), tuning.RequestTimeout)
	defer cancel()
	res := h.answerer.Answer(ctx, q)

	c.Set(middleware.KeyMode, string(res.Mode))
	c.Set(middleware.KeyCached, res.Cached)
	c.Set(middleware.KeyLLMCalls, res.LLMCalls)
	c.Set(middleware.KeyInTok, res.InTok)
	c.Set(middleware.KeyOutTok, res.OutTok)
	c.Header("X-Ask-Mode", string(res.Mode))
	c.Header("X-Index-Version", h.health.IndexVersion)
	sources := res.Sources
	if sources == nil {
		sources = []string{}
	}
	c.JSON(http.StatusOK, AskResponse{Answer: res.Answer, Sources: sources})
}

func (h *handler) healthz(c *gin.Context) {
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, h.health)
}

func (h *handler) root(c *gin.Context) {
	if c.Request.Method == http.MethodHead {
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.Status(http.StatusOK)
		return
	}
	c.String(http.StatusOK, "Ask OLake API. POST /ask")
}

func widget(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, "application/javascript; charset=utf-8", web.WidgetJS)
}
