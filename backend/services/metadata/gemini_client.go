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

type geminiClient struct {
	apiKey      string
	httpc       *http.Client
	cache       *fileCache
	throttleMu  sync.Mutex
	lastRequest time.Time
	minInterval time.Duration
}

func newGeminiClient(apiKey string, httpc *http.Client, cache *fileCache) *geminiClient {
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	return &geminiClient{
		apiKey:      strings.TrimSpace(apiKey),
		httpc:       httpc,
		cache:       cache,
		minInterval: 100 * time.Millisecond,
	}
}

func (c *geminiClient) isConfigured() bool {
	return c.apiKey != ""
}

// geminiRequest is the request body for the Gemini generateContent API.
type geminiRequest struct {
	Contents         []geminiContent        `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	Temperature     float64 `json:"temperature"`
	MaxOutputTokens int     `json:"maxOutputTokens"`
	ResponseMIMEType string `json:"responseMimeType,omitempty"`
}

// geminiResponse is the response from the Gemini generateContent API.
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

// GeminiRecommendation is a single recommendation returned by Gemini.
type GeminiRecommendation struct {
	Title     string `json:"title"`
	Year      int    `json:"year"`
	MediaType string `json:"mediaType"` // "movie" or "series"
}

// getRecommendations asks Gemini for personalized recommendations based on watched titles.
func (c *geminiClient) getRecommendations(ctx context.Context, watchedTitles []string, mediaTypes []string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, errors.New("gemini api key not configured")
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

	prompt := fmt.Sprintf(`You are a movie and TV show recommendation engine. Based on the following titles that a user has recently watched and enjoyed, recommend exactly 20 movies and TV shows they would likely enjoy. Mix both movies and TV shows in your recommendations.

Titles the user has watched:
%s
Focus on:
- Similar genres, themes, and tone
- Both well-known and hidden gem recommendations
- A mix of recent and classic titles
- Do NOT recommend titles the user has already watched (listed above)

Respond with ONLY a JSON array, no other text. Each object must have exactly these fields:
- "title": the exact title as it appears on TMDB
- "year": the release year (integer)
- "mediaType": either "movie" or "series"

Example format:
[{"title": "Inception", "year": 2010, "mediaType": "movie"}, {"title": "Dark", "year": 2017, "mediaType": "series"}]`, titleList)

	// Rate limiting
	c.throttleMu.Lock()
	since := time.Since(c.lastRequest)
	if since < c.minInterval {
		time.Sleep(c.minInterval - since)
	}
	c.lastRequest = time.Now()
	c.throttleMu.Unlock()

	// Build request - use gemma-3n-e4b-it (free tier, fast, good at structured output)
	endpoint := fmt.Sprintf("%s/models/gemma-3n-e4b-it:generateContent?key=%s", geminiBaseURL, c.apiKey)

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
		GenerationConfig: &geminiGenerationConfig{
			Temperature:     0.7,
			MaxOutputTokens: 2048,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Retry with backoff
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// Re-create body reader for retry
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[gemini] http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("gemini request failed: status %d", resp.StatusCode)
			log.Printf("[gemini] rate limited or server error (attempt %d/3): status %d", attempt+1, resp.StatusCode)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("gemini API error %d: %s", resp.StatusCode, string(body))
		}

		var geminiResp geminiResponse
		if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
			return nil, fmt.Errorf("decode gemini response: %w", err)
		}

		if geminiResp.Error != nil {
			return nil, fmt.Errorf("gemini API error: %s", geminiResp.Error.Message)
		}

		if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
			return nil, errors.New("gemini returned empty response")
		}

		// Parse the JSON response
		responseText := geminiResp.Candidates[0].Content.Parts[0].Text

		var recommendations []GeminiRecommendation
		if err := json.Unmarshal([]byte(responseText), &recommendations); err != nil {
			// Try to extract JSON from potential markdown code block
			cleaned := strings.TrimSpace(responseText)
			cleaned = strings.TrimPrefix(cleaned, "```json")
			cleaned = strings.TrimPrefix(cleaned, "```")
			cleaned = strings.TrimSuffix(cleaned, "```")
			cleaned = strings.TrimSpace(cleaned)
			if err2 := json.Unmarshal([]byte(cleaned), &recommendations); err2 != nil {
				return nil, fmt.Errorf("parse gemini recommendations: %w (raw: %s)", err, responseText[:min(200, len(responseText))])
			}
		}

		return recommendations, nil
	}

	return nil, fmt.Errorf("gemini request failed after 3 attempts: %w", lastErr)
}

// getSimilarRecommendations asks Gemini for recommendations similar to a specific title.
func (c *geminiClient) getSimilarRecommendations(ctx context.Context, seedTitle string, mediaType string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, errors.New("gemini api key not configured")
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

	// Rate limiting
	c.throttleMu.Lock()
	since := time.Since(c.lastRequest)
	if since < c.minInterval {
		time.Sleep(c.minInterval - since)
	}
	c.lastRequest = time.Now()
	c.throttleMu.Unlock()

	endpoint := fmt.Sprintf("%s/models/gemma-3n-e4b-it:generateContent?key=%s", geminiBaseURL, c.apiKey)

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
		GenerationConfig: &geminiGenerationConfig{
			Temperature:     0.7,
			MaxOutputTokens: 2048,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[gemini] similar http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("gemini request failed: status %d", resp.StatusCode)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("gemini API error %d: %s", resp.StatusCode, string(body))
		}

		var geminiResp geminiResponse
		if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
			return nil, fmt.Errorf("decode gemini response: %w", err)
		}

		if geminiResp.Error != nil {
			return nil, fmt.Errorf("gemini API error: %s", geminiResp.Error.Message)
		}

		if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
			return nil, errors.New("gemini returned empty response")
		}

		responseText := geminiResp.Candidates[0].Content.Parts[0].Text
		var recommendations []GeminiRecommendation
		if err := json.Unmarshal([]byte(responseText), &recommendations); err != nil {
			cleaned := strings.TrimSpace(responseText)
			cleaned = strings.TrimPrefix(cleaned, "```json")
			cleaned = strings.TrimPrefix(cleaned, "```")
			cleaned = strings.TrimSuffix(cleaned, "```")
			cleaned = strings.TrimSpace(cleaned)
			if err2 := json.Unmarshal([]byte(cleaned), &recommendations); err2 != nil {
				return nil, fmt.Errorf("parse gemini similar recommendations: %w (raw: %s)", err, responseText[:min(200, len(responseText))])
			}
		}

		return recommendations, nil
	}

	return nil, fmt.Errorf("gemini similar request failed after 3 attempts: %w", lastErr)
}

// getCustomRecommendations asks Gemini for recommendations based on a free-text user query.
func (c *geminiClient) getCustomRecommendations(ctx context.Context, query string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, errors.New("gemini api key not configured")
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

	c.throttleMu.Lock()
	since := time.Since(c.lastRequest)
	if since < c.minInterval {
		time.Sleep(c.minInterval - since)
	}
	c.lastRequest = time.Now()
	c.throttleMu.Unlock()

	endpoint := fmt.Sprintf("%s/models/gemma-3n-e4b-it:generateContent?key=%s", geminiBaseURL, c.apiKey)

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
		GenerationConfig: &geminiGenerationConfig{
			Temperature:     0.8,
			MaxOutputTokens: 2048,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[gemini] custom http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("gemini request failed: status %d", resp.StatusCode)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("gemini API error %d: %s", resp.StatusCode, string(body))
		}

		var geminiResp geminiResponse
		if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
			return nil, fmt.Errorf("decode gemini response: %w", err)
		}

		if geminiResp.Error != nil {
			return nil, fmt.Errorf("gemini API error: %s", geminiResp.Error.Message)
		}

		if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
			return nil, errors.New("gemini returned empty response")
		}

		responseText := geminiResp.Candidates[0].Content.Parts[0].Text
		var recommendations []GeminiRecommendation
		if err := json.Unmarshal([]byte(responseText), &recommendations); err != nil {
			cleaned := strings.TrimSpace(responseText)
			cleaned = strings.TrimPrefix(cleaned, "```json")
			cleaned = strings.TrimPrefix(cleaned, "```")
			cleaned = strings.TrimSuffix(cleaned, "```")
			cleaned = strings.TrimSpace(cleaned)
			if err2 := json.Unmarshal([]byte(cleaned), &recommendations); err2 != nil {
				return nil, fmt.Errorf("parse gemini custom recommendations: %w (raw: %s)", err, responseText[:min(200, len(responseText))])
			}
		}

		return recommendations, nil
	}

	return nil, fmt.Errorf("gemini custom request failed after 3 attempts: %w", lastErr)
}

// getSurpriseRecommendation asks Gemini for a single random movie/show recommendation.
// Uses high temperature and randomized prompt elements to avoid repetitive answers.
func (c *geminiClient) getSurpriseRecommendation(ctx context.Context, preferredDecade, preferredMediaType string) ([]GeminiRecommendation, error) {
	if !c.isConfigured() {
		return nil, errors.New("gemini api key not configured")
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

	c.throttleMu.Lock()
	since := time.Since(c.lastRequest)
	if since < c.minInterval {
		time.Sleep(c.minInterval - since)
	}
	c.lastRequest = time.Now()
	c.throttleMu.Unlock()

	endpoint := fmt.Sprintf("%s/models/gemma-3n-e4b-it:generateContent?key=%s", geminiBaseURL, c.apiKey)

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
		GenerationConfig: &geminiGenerationConfig{
			Temperature:     1.5,
			MaxOutputTokens: 256,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[gemini] surprise http error (attempt %d/3): %v", attempt+1, err)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("gemini request failed: status %d", resp.StatusCode)
			time.Sleep(backoff)
			backoff *= 2
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("gemini API error %d: %s", resp.StatusCode, string(body))
		}

		var geminiResp geminiResponse
		if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
			return nil, fmt.Errorf("decode gemini response: %w", err)
		}

		if geminiResp.Error != nil {
			return nil, fmt.Errorf("gemini API error: %s", geminiResp.Error.Message)
		}

		if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
			return nil, errors.New("gemini returned empty response")
		}

		responseText := geminiResp.Candidates[0].Content.Parts[0].Text
		var recommendations []GeminiRecommendation
		if err := json.Unmarshal([]byte(responseText), &recommendations); err != nil {
			cleaned := strings.TrimSpace(responseText)
			cleaned = strings.TrimPrefix(cleaned, "```json")
			cleaned = strings.TrimPrefix(cleaned, "```")
			cleaned = strings.TrimSuffix(cleaned, "```")
			cleaned = strings.TrimSpace(cleaned)
			if err2 := json.Unmarshal([]byte(cleaned), &recommendations); err2 != nil {
				return nil, fmt.Errorf("parse gemini surprise: %w (raw: %s)", err, responseText[:min(200, len(responseText))])
			}
		}

		return recommendations, nil
	}

	return nil, fmt.Errorf("gemini surprise request failed after 3 attempts: %w", lastErr)
}
