package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

const (
	aiProviderGemini     = "gemini"
	aiProviderOpenAI     = "openai"
	aiProviderAnthropic  = "anthropic"
	aiProviderOpenRouter = "openrouter"
	aiProviderNanoGPT    = "nanogpt"
	aiProviderLinkAPI    = "linkapi"
)

type AIConfig struct {
	Provider string
	APIKey   string
	Model    string
	BaseURL  string
}

type geminiClient struct {
	provider    string
	apiKey      string
	model       string
	baseURL     string
	httpc       *http.Client
	cache       *fileCache
	throttleMu  sync.Mutex
	lastRequest time.Time
	minInterval time.Duration
}

func newGeminiClient(apiKey string, httpc *http.Client, cache *fileCache) *geminiClient {
	return newAIClient(AIConfig{Provider: aiProviderGemini, APIKey: apiKey}, httpc, cache)
}

func newAIClient(cfg AIConfig, httpc *http.Client, cache *fileCache) *geminiClient {
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	provider := normalizeMetadataAIProvider(cfg.Provider, cfg.APIKey)
	return &geminiClient{
		provider:    provider,
		apiKey:      strings.TrimSpace(cfg.APIKey),
		model:       strings.TrimSpace(cfg.Model),
		baseURL:     strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		httpc:       httpc,
		cache:       cache,
		minInterval: 100 * time.Millisecond,
	}
}

func (c *geminiClient) isConfigured() bool {
	return c != nil && c.apiKey != "" && c.provider != ""
}

func normalizeMetadataAIProvider(provider, apiKey string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "none":
		if strings.TrimSpace(apiKey) == "" {
			return ""
		}
		return aiProviderGemini
	case "gemini", "google", "google-gemini":
		return aiProviderGemini
	case "openai", "chatgpt", "gpt":
		return aiProviderOpenAI
	case "anthropic", "claude":
		return aiProviderAnthropic
	case "openrouter", "open-router":
		return aiProviderOpenRouter
	case "nanogpt", "nano-gpt", "nano_gpt":
		return aiProviderNanoGPT
	case "linkapi", "link-api", "link_api":
		return aiProviderLinkAPI
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func (c *geminiClient) providerLabel() string {
	switch c.provider {
	case aiProviderOpenAI:
		return "OpenAI"
	case aiProviderAnthropic:
		return "Anthropic"
	case aiProviderOpenRouter:
		return "OpenRouter"
	case aiProviderNanoGPT:
		return "NanoGPT"
	case aiProviderLinkAPI:
		return "LinkAPI"
	default:
		return "Gemini"
	}
}

func (c *geminiClient) defaultModel() string {
	switch c.provider {
	case aiProviderOpenAI:
		return "gpt-5.5"
	case aiProviderAnthropic:
		return "claude-sonnet-4-5"
	case aiProviderOpenRouter:
		return "~openai/gpt-latest"
	case aiProviderNanoGPT, aiProviderLinkAPI:
		return "gpt-4o-mini"
	default:
		return "gemma-4-26b-a4b-it"
	}
}

func (c *geminiClient) modelName() string {
	if strings.TrimSpace(c.model) != "" {
		return strings.TrimSpace(c.model)
	}
	return c.defaultModel()
}

func (c *geminiClient) defaultBaseURL() string {
	switch c.provider {
	case aiProviderOpenAI:
		return "https://api.openai.com/v1"
	case aiProviderAnthropic:
		return "https://api.anthropic.com/v1"
	case aiProviderOpenRouter:
		return "https://openrouter.ai/api/v1"
	case aiProviderNanoGPT:
		return "https://nano-gpt.com/api/v1"
	case aiProviderLinkAPI:
		return "https://api.linkapi.org/v1"
	default:
		return geminiBaseURL
	}
}

func (c *geminiClient) resolvedBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return c.defaultBaseURL()
}

// geminiRequest is the request body for the Gemini generateContent API.
type geminiRequest struct {
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	Contents          []geminiContent         `json:"contents"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

// noThinkSystem is the system instruction that suppresses Gemma 4 thinking tokens.
var noThinkSystem = &geminiContent{Parts: []geminiPart{{Text: "You are a helpful assistant. no thought tokens. Respond directly without reasoning."}}}

// noThinkPrompt wraps a prompt with the <thought off> prefix that, combined with
// noThinkSystem, reliably suppresses the Gemma 4 thinking chain (~14s vs ~36s).
func noThinkPrompt(prompt string) string {
	return "<thought off> " + prompt
}

type geminiGenerationConfig struct {
	Temperature      float64 `json:"temperature"`
	MaxOutputTokens  int     `json:"maxOutputTokens"`
	ResponseMIMEType string  `json:"responseMimeType,omitempty"`
}

// geminiResponse is the response from the Gemini generateContent API.
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

// geminiResponseText returns the first non-thought part text from a response.
// Gemma 4 thinking models split output into a thought part (reasoning chain)
// and an answer part — we always want the answer.
func geminiResponseText(resp geminiResponse) (string, error) {
	if len(resp.Candidates) == 0 {
		return "", errors.New("gemini returned empty response")
	}
	parts := resp.Candidates[0].Content.Parts
	for _, p := range parts {
		if !p.Thought {
			return p.Text, nil
		}
	}
	if len(parts) > 0 {
		return parts[len(parts)-1].Text, nil
	}
	return "", errors.New("gemini returned empty response")
}

type openAIChatRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Temperature float64             `json:"temperature,omitempty"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// GeminiRecommendation is a single recommendation returned by the configured AI provider.
type GeminiRecommendation struct {
	Title     string `json:"title"`
	Year      int    `json:"year"`
	MediaType string `json:"mediaType"` // "movie" or "series"
}

func (c *geminiClient) completeRecommendations(ctx context.Context, prompt string, temperature float64, maxTokens int, label string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, fmt.Errorf("%s api key not configured", strings.ToLower(c.providerLabel()))
	}

	c.throttleMu.Lock()
	wait := c.minInterval - time.Since(c.lastRequest)
	if wait > 0 {
		c.lastRequest = time.Now().Add(wait)
	} else {
		c.lastRequest = time.Now()
		wait = 0
	}
	c.throttleMu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}

	var responseText string
	var err error
	switch c.provider {
	case aiProviderOpenAI, aiProviderOpenRouter, aiProviderNanoGPT, aiProviderLinkAPI:
		responseText, err = c.completeOpenAICompatible(ctx, prompt, temperature, maxTokens, label)
	case aiProviderAnthropic:
		responseText, err = c.completeAnthropic(ctx, prompt, temperature, maxTokens, label)
	default:
		responseText, err = c.completeGemini(ctx, prompt, temperature, maxTokens, label)
	}
	if err != nil {
		return nil, err
	}
	return parseAIRecommendations(responseText, c.providerLabel(), label)
}

func (c *geminiClient) completeGemini(ctx context.Context, prompt string, temperature float64, maxTokens int, label string) (string, error) {
	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.resolvedBaseURL(), c.modelName(), c.apiKey)
	reqBody := geminiRequest{
		SystemInstruction: noThinkSystem,
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: noThinkPrompt(prompt)}}},
		},
		GenerationConfig: &geminiGenerationConfig{
			Temperature:     temperature,
			MaxOutputTokens: maxTokens,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal gemini request: %w", err)
	}

	var geminiResp geminiResponse
	if err := c.doJSONWithRetry(ctx, http.MethodPost, endpoint, nil, bodyBytes, &geminiResp, "gemini", label); err != nil {
		return "", err
	}
	if geminiResp.Error != nil {
		return "", fmt.Errorf("gemini API error: %s", geminiResp.Error.Message)
	}
	return geminiResponseText(geminiResp)
}

func (c *geminiClient) completeOpenAICompatible(ctx context.Context, prompt string, temperature float64, maxTokens int, label string) (string, error) {
	endpoint := c.resolvedBaseURL() + "/chat/completions"
	reqBody := openAIChatRequest{
		Model: c.modelName(),
		Messages: []openAIChatMessage{
			{Role: "system", Content: "You are a movie and TV show recommendation engine. Respond only with the requested JSON array."},
			{Role: "user", Content: prompt},
		},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal %s request: %w", c.providerLabel(), err)
	}
	headers := map[string]string{"Authorization": "Bearer " + c.apiKey}
	if c.provider == aiProviderOpenRouter {
		headers["HTTP-Referer"] = "https://github.com/godver3/mediastorm"
		headers["X-OpenRouter-Title"] = "mediastorm"
	}

	var chatResp openAIChatResponse
	if err := c.doJSONWithRetry(ctx, http.MethodPost, endpoint, headers, bodyBytes, &chatResp, strings.ToLower(c.providerLabel()), label); err != nil {
		return "", err
	}
	if chatResp.Error != nil {
		return "", fmt.Errorf("%s API error: %s", c.providerLabel(), chatResp.Error.Message)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("%s returned empty response", c.providerLabel())
	}
	return chatResp.Choices[0].Message.Content, nil
}

func (c *geminiClient) completeAnthropic(ctx context.Context, prompt string, temperature float64, maxTokens int, label string) (string, error) {
	endpoint := c.resolvedBaseURL() + "/messages"
	reqBody := anthropicRequest{
		Model:       c.modelName(),
		MaxTokens:   maxTokens,
		Temperature: temperature,
		System:      "You are a movie and TV show recommendation engine. Respond only with the requested JSON array.",
		Messages: []anthropicMessage{
			{Role: "user", Content: prompt},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal Anthropic request: %w", err)
	}
	headers := map[string]string{
		"x-api-key":         c.apiKey,
		"anthropic-version": "2023-06-01",
	}

	var msgResp anthropicResponse
	if err := c.doJSONWithRetry(ctx, http.MethodPost, endpoint, headers, bodyBytes, &msgResp, "anthropic", label); err != nil {
		return "", err
	}
	if msgResp.Error != nil {
		return "", fmt.Errorf("Anthropic API error: %s", msgResp.Error.Message)
	}
	for _, part := range msgResp.Content {
		if part.Text != "" {
			return part.Text, nil
		}
	}
	return "", errors.New("Anthropic returned empty response")
}

func (c *geminiClient) doJSONWithRetry(ctx context.Context, method, endpoint string, headers map[string]string, bodyBytes []byte, out interface{}, logPrefix, label string) error {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("create %s request: %w", logPrefix, err)
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[%s] %s http error (attempt %d/3): %v", logPrefix, label, attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("%s request failed: status %d", logPrefix, resp.StatusCode)
			log.Printf("[%s] %s retryable status (attempt %d/3): %d", logPrefix, label, attempt+1, resp.StatusCode)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("%s API error %d: %s", c.providerLabel(), resp.StatusCode, string(body))
		}

		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode %s response: %w", logPrefix, err)
		}
		return nil
	}
	return fmt.Errorf("%s request failed after 3 attempts: %w", c.providerLabel(), lastErr)
}

func parseAIRecommendations(responseText, providerLabel, label string) ([]GeminiRecommendation, error) {
	var recommendations []GeminiRecommendation
	if err := json.Unmarshal([]byte(responseText), &recommendations); err != nil {
		cleaned := strings.TrimSpace(responseText)
		cleaned = strings.TrimPrefix(cleaned, "```json")
		cleaned = strings.TrimPrefix(cleaned, "```")
		cleaned = strings.TrimSuffix(cleaned, "```")
		cleaned = strings.TrimSpace(cleaned)
		if err2 := json.Unmarshal([]byte(cleaned), &recommendations); err2 != nil {
			return nil, fmt.Errorf("parse %s %s recommendations: %w (raw: %s)", providerLabel, label, err, responseText[:min(200, len(responseText))])
		}
	}
	return recommendations, nil
}

// getRecommendations asks the configured AI provider for personalized recommendations based on watched titles.
func (c *geminiClient) getRecommendations(ctx context.Context, watchedTitles []string, mediaTypes []string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, errors.New("AI provider API key not configured")
	}

	if len(watchedTitles) == 0 {
		return nil, errors.New("no watched titles provided")
	}

	// Build the prompt
	titleList := ""
	for i, title := range watchedTitles {
		mt := "movie"
		if i < len(mediaTypes) {
			mt = mediaTypes[i]
		}
		titleList += fmt.Sprintf("- %s (%s)\n", title, mt)
	}

	// Randomized emphasis to produce varied recommendations each time
	emphases := []string{
		"Prioritize hidden gems and lesser-known titles that most people haven't seen.",
		"Lean toward critically acclaimed titles with strong storytelling.",
		"Focus on visually stunning and atmospheric picks.",
		"Emphasize cult classics and fan favorites.",
		"Include some surprising, unexpected picks from genres the user might not expect to enjoy.",
		"Lean toward recent releases from the last few years.",
		"Include a mix of international and foreign-language titles alongside English ones.",
		"Prioritize character-driven stories with strong performances.",
	}
	emphasis := emphases[time.Now().UnixNano()%int64(len(emphases))]

	prompt := fmt.Sprintf(`You are a movie and TV show recommendation engine. Based on the following titles that a user has recently watched and enjoyed, recommend exactly 20 movies and TV shows they would likely enjoy.

IMPORTANT: You MUST include a balanced mix — at least 8 movies and at least 8 TV shows in your 20 recommendations. Do not skew heavily toward one type.

Titles the user has watched:
%s
Focus on:
- Similar genres, themes, and tone
- Both well-known and hidden gem recommendations
- A mix of recent and classic titles
- Do NOT recommend titles the user has already watched (listed above)
- %s

Respond with ONLY a JSON array, no other text. Each object must have exactly these fields:
- "title": the exact title as it appears on TMDB
- "year": the release year (integer)
- "mediaType": either "movie" or "series"

	Example format:
	[{"title": "Inception", "year": 2010, "mediaType": "movie"}, {"title": "Dark", "year": 2017, "mediaType": "series"}]`, titleList, emphasis)

	return c.completeRecommendations(ctx, prompt, 0.9, 2048, "recommendations")
}

// getSimilarRecommendations asks the configured AI provider for recommendations similar to a specific title.
func (c *geminiClient) getSimilarRecommendations(ctx context.Context, seedTitle string, mediaType string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, errors.New("AI provider API key not configured")
	}

	prompt := fmt.Sprintf(`You are a movie and TV show recommendation engine. A user loved "%s" (%s). Recommend exactly 15 movies and TV shows they would enjoy based on this title.

Focus on:
- Similar genres, themes, tone, and style
- Both well-known and hidden gem recommendations
- A mix of recent and classic titles
- Do NOT recommend "%s" itself

Respond with ONLY a JSON array, no other text. Each object must have exactly these fields:
- "title": the exact title as it appears on TMDB
- "year": the release year (integer)
- "mediaType": either "movie" or "series"

	Example format:
	[{"title": "Inception", "year": 2010, "mediaType": "movie"}, {"title": "Dark", "year": 2017, "mediaType": "series"}]`, seedTitle, mediaType, seedTitle)

	return c.completeRecommendations(ctx, prompt, 0.7, 2048, "similar")
}

// getCustomRecommendations asks the configured AI provider for recommendations based on a free-text user query.
func (c *geminiClient) getCustomRecommendations(ctx context.Context, query string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, errors.New("AI provider API key not configured")
	}

	prompt := fmt.Sprintf(`You are a movie and TV show recommendation engine. A user has made the following request:

"%s"

Based on this request, recommend exactly 20 movies and TV shows that best match what the user is looking for. Every result MUST be directly relevant to the request. If the user asks about a specific actor, director, or franchise, ONLY include titles featuring that person or from that franchise. Do NOT include loosely related or thematically similar titles that don't match the specific request.

Respond with ONLY a JSON array, no other text. Each object must have exactly these fields:
- "title": the exact official title as listed on TMDB (The Movie Database)
- "year": the original release year (integer)
- "mediaType": either "movie" or "series"

	Example format:
	[{"title": "Inception", "year": 2010, "mediaType": "movie"}, {"title": "Dark", "year": 2017, "mediaType": "series"}]`, query)

	return c.completeRecommendations(ctx, prompt, 0.8, 2048, "custom")
}

// getSurpriseRecommendation asks the configured AI provider for a single random movie/show recommendation.
// Uses high temperature and randomized prompt elements to avoid repetitive answers.
func (c *geminiClient) getSurpriseRecommendation(ctx context.Context, preferredDecade, preferredMediaType string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, errors.New("AI provider API key not configured")
	}

	// Randomized category seeds to push Gemma toward variety
	vibes := []string{
		"an underrated hidden gem", "a cult classic", "a critically acclaimed masterpiece",
		"a mind-bending thriller", "a heartwarming drama", "a visually stunning film",
		"a dark comedy", "an edge-of-your-seat suspense film", "a thought-provoking sci-fi",
		"a gripping crime story", "an epic adventure", "a quirky indie film",
		"a powerful war film", "a feel-good movie", "a chilling horror film",
		"a romantic classic", "an animated masterpiece", "a biographical drama",
	}

	now := time.Now().UnixNano()
	vibe := vibes[now%int64(len(vibes))]

	// Use provided decade or pick a random one
	decade := preferredDecade
	if decade == "" {
		decades := []string{"1960s", "1970s", "1980s", "1990s", "2000s", "2010s", "2020s"}
		decade = decades[(now/7)%int64(len(decades))]
	}

	// Determine media type constraint for prompt
	var mediaTypeConstraint, mediaTypeExample string
	switch preferredMediaType {
	case "movie":
		mediaTypeConstraint = "movie (NOT a TV show)"
		mediaTypeExample = "movie"
	case "show":
		mediaTypeConstraint = "TV show (NOT a movie)"
		mediaTypeExample = "series"
	default:
		mediaTypeConstraint = "movie or TV show"
		mediaTypeExample = "movie"
	}

	prompt := fmt.Sprintf(`Pick exactly 1 random %s recommendation. Be creative and surprising — do NOT pick an obvious or mainstream choice. Surprise the user with something unexpected and excellent.

Constraints:
- It should be %s from the %s
- It MUST be a %s
- It must be a real title available on TMDB (The Movie Database)
- Do NOT pick the same title you would normally default to — think outside the box

	Respond with ONLY a JSON array containing exactly 1 object, no other text:
	[{"title": "exact TMDB title", "year": 1234, "mediaType": "%s"}]`, mediaTypeConstraint, vibe, decade, mediaTypeConstraint, mediaTypeExample)

	return c.completeRecommendations(ctx, prompt, 1.5, 256, "surprise")
}
