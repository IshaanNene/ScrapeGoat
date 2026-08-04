package fetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CAPTCHASolver integrates with external CAPTCHA solving services.
type CAPTCHASolver struct {
	provider string
	apiKey   string
	endpoint string
	client   *http.Client
	logger   *slog.Logger
}

// CAPTCHAType identifies the type of CAPTCHA.
type CAPTCHAType string

const (
	CAPTCHAReCaptchaV2 CAPTCHAType = "recaptcha_v2"
	CAPTCHAReCaptchaV3 CAPTCHAType = "recaptcha_v3"
	CAPTCHAHCaptcha    CAPTCHAType = "hcaptcha"
	CAPTCHATurnstile   CAPTCHAType = "turnstile"
	CAPTCHAImage       CAPTCHAType = "image"
)

// CAPTCHARequest contains info needed to solve a CAPTCHA.
type CAPTCHARequest struct {
	Type      CAPTCHAType `json:"type"`
	SiteKey   string      `json:"site_key,omitempty"`
	SiteURL   string      `json:"site_url"`
	ImageData string      `json:"image_data,omitempty"` // Base64 for image CAPTCHAs
	Action    string      `json:"action,omitempty"`     // reCAPTCHA v3 action
	MinScore  float64     `json:"min_score,omitempty"`  // reCAPTCHA v3 minimum score
	Invisible bool        `json:"invisible,omitempty"`  // Invisible reCAPTCHA
}

// CAPTCHAResponse contains the solution from the solving service.
type CAPTCHAResponse struct {
	Solution string  `json:"solution"`
	TaskID   string  `json:"task_id"`
	Cost     float64 `json:"cost"`
	Duration time.Duration
}

// NewCAPTCHASolver creates a new CAPTCHA solving integration.
// Supports: 2captcha, anti-captcha, capsolver
func NewCAPTCHASolver(provider, apiKey, endpoint string, logger *slog.Logger) *CAPTCHASolver {
	if endpoint == "" {
		switch provider {
		case "2captcha":
			endpoint = "https://2captcha.com/in.php"
		case "anti-captcha":
			endpoint = "https://api.anti-captcha.com"
		case "capsolver":
			endpoint = "https://api.capsolver.com"
		}
	}

	return &CAPTCHASolver{
		provider: provider,
		apiKey:   apiKey,
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		logger: logger.With("component", "captcha_solver"),
	}
}

// Solve sends a CAPTCHA to the solving service and returns the solution.
func (cs *CAPTCHASolver) Solve(ctx context.Context, req *CAPTCHARequest) (*CAPTCHAResponse, error) {
	start := time.Now()
	cs.logger.Info("solving captcha", "type", req.Type, "url", req.SiteURL)

	switch cs.provider {
	case "2captcha":
		return cs.solve2Captcha(ctx, req, start)
	case "anti-captcha":
		return cs.solveAntiCaptcha(ctx, req, start)
	case "capsolver":
		return cs.solveCapsolver(ctx, req, start)
	default:
		return nil, fmt.Errorf("unsupported CAPTCHA provider: %s", cs.provider)
	}
}

// solve2Captcha implements the 2captcha.com API.
func (cs *CAPTCHASolver) solve2Captcha(ctx context.Context, req *CAPTCHARequest, start time.Time) (*CAPTCHAResponse, error) {
	// Submit task
	params := url.Values{
		"key":     {cs.apiKey},
		"json":    {"1"},
		"pageurl": {req.SiteURL},
	}

	switch req.Type {
	case CAPTCHAReCaptchaV2:
		params.Set("method", "userrecaptcha")
		params.Set("googlekey", req.SiteKey)
		if req.Invisible {
			params.Set("invisible", "1")
		}
	case CAPTCHAReCaptchaV3:
		params.Set("method", "userrecaptcha")
		params.Set("version", "v3")
		params.Set("googlekey", req.SiteKey)
		if req.Action != "" {
			params.Set("action", req.Action)
		}
		if req.MinScore > 0 {
			params.Set("min_score", fmt.Sprintf("%.1f", req.MinScore))
		}
	case CAPTCHAHCaptcha:
		params.Set("method", "hcaptcha")
		params.Set("sitekey", req.SiteKey)
	case CAPTCHATurnstile:
		params.Set("method", "turnstile")
		params.Set("sitekey", req.SiteKey)
	case CAPTCHAImage:
		params.Set("method", "base64")
		params.Set("body", req.ImageData)
	default:
		return nil, fmt.Errorf("unsupported CAPTCHA type: %s", req.Type)
	}

	// Submit
	submitResp, err := cs.client.PostForm(cs.endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("submit captcha: %w", err)
	}
	defer submitResp.Body.Close()

	var submitResult struct {
		Status  int    `json:"status"`
		Request string `json:"request"`
	}
	if err := json.NewDecoder(submitResp.Body).Decode(&submitResult); err != nil {
		return nil, fmt.Errorf("decode submit response: %w", err)
	}
	if submitResult.Status != 1 {
		return nil, fmt.Errorf("captcha submit failed: %s", submitResult.Request)
	}

	taskID := submitResult.Request

	// Poll for result
	resultEndpoint := strings.Replace(cs.endpoint, "/in.php", "/res.php", 1)
	pollParams := url.Values{
		"key":    {cs.apiKey},
		"action": {"get"},
		"id":     {taskID},
		"json":   {"1"},
	}

	for i := 0; i < 60; i++ { // Max 5 minutes
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}

		pollResp, err := cs.client.Get(resultEndpoint + "?" + pollParams.Encode())
		if err != nil {
			continue
		}

		body, _ := io.ReadAll(pollResp.Body)
		pollResp.Body.Close()

		var result struct {
			Status  int    `json:"status"`
			Request string `json:"request"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			continue
		}

		if result.Status == 1 {
			return &CAPTCHAResponse{
				Solution: result.Request,
				TaskID:   taskID,
				Duration: time.Since(start),
			}, nil
		}

		if result.Request != "CAPCHA_NOT_READY" {
			return nil, fmt.Errorf("captcha solve error: %s", result.Request)
		}
	}

	return nil, fmt.Errorf("captcha solve timeout after %s", time.Since(start))
}

// solveAntiCaptcha implements the anti-captcha.com API.
func (cs *CAPTCHASolver) solveAntiCaptcha(ctx context.Context, req *CAPTCHARequest, start time.Time) (*CAPTCHAResponse, error) {
	// Create task
	taskPayload := map[string]any{
		"clientKey": cs.apiKey,
	}

	task := map[string]any{
		"websiteURL": req.SiteURL,
	}

	switch req.Type {
	case CAPTCHAReCaptchaV2:
		task["type"] = "RecaptchaV2TaskProxyless"
		task["websiteKey"] = req.SiteKey
	case CAPTCHAReCaptchaV3:
		task["type"] = "RecaptchaV3TaskProxyless"
		task["websiteKey"] = req.SiteKey
		task["minScore"] = req.MinScore
		task["pageAction"] = req.Action
	case CAPTCHAHCaptcha:
		task["type"] = "HCaptchaTaskProxyless"
		task["websiteKey"] = req.SiteKey
	default:
		return nil, fmt.Errorf("unsupported type for anti-captcha: %s", req.Type)
	}

	taskPayload["task"] = task

	body, _ := json.Marshal(taskPayload)
	resp, err := cs.client.Post(cs.endpoint+"/createTask", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	defer resp.Body.Close()

	var createResult struct {
		ErrorID   int    `json:"errorId"`
		TaskID    int    `json:"taskId"`
		ErrorDesc string `json:"errorDescription"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResult); err != nil {
		return nil, err
	}
	if createResult.ErrorID != 0 {
		return nil, fmt.Errorf("anti-captcha error: %s", createResult.ErrorDesc)
	}

	// Poll for result
	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}

		pollBody, _ := json.Marshal(map[string]any{
			"clientKey": cs.apiKey,
			"taskId":    createResult.TaskID,
		})

		pollResp, err := cs.client.Post(cs.endpoint+"/getTaskResult", "application/json", strings.NewReader(string(pollBody)))
		if err != nil {
			continue
		}

		var result struct {
			Status   string `json:"status"`
			Solution struct {
				GRecaptchaResponse string `json:"gRecaptchaResponse"`
				Token              string `json:"token"`
			} `json:"solution"`
			Cost string `json:"cost"`
		}
		if err := json.NewDecoder(pollResp.Body).Decode(&result); err != nil {
			pollResp.Body.Close()
			continue
		}
		pollResp.Body.Close()

		if result.Status == "ready" {
			solution := result.Solution.GRecaptchaResponse
			if solution == "" {
				solution = result.Solution.Token
			}
			return &CAPTCHAResponse{
				Solution: solution,
				TaskID:   fmt.Sprintf("%d", createResult.TaskID),
				Duration: time.Since(start),
			}, nil
		}
	}

	return nil, fmt.Errorf("anti-captcha timeout")
}

// solveCapsolver implements the capsolver.com API.
func (cs *CAPTCHASolver) solveCapsolver(ctx context.Context, req *CAPTCHARequest, start time.Time) (*CAPTCHAResponse, error) {
	task := map[string]any{
		"clientKey": cs.apiKey,
	}

	taskContent := map[string]any{
		"websiteURL": req.SiteURL,
		"websiteKey": req.SiteKey,
	}

	switch req.Type {
	case CAPTCHAReCaptchaV2:
		taskContent["type"] = "ReCaptchaV2TaskProxyLess"
	case CAPTCHAReCaptchaV3:
		taskContent["type"] = "ReCaptchaV3TaskProxyLess"
		taskContent["pageAction"] = req.Action
	case CAPTCHAHCaptcha:
		taskContent["type"] = "HCaptchaTaskProxyless"
	case CAPTCHATurnstile:
		taskContent["type"] = "AntiTurnstileTaskProxyLess"
	default:
		return nil, fmt.Errorf("unsupported type for capsolver: %s", req.Type)
	}

	task["task"] = taskContent
	body, _ := json.Marshal(task)

	resp, err := cs.client.Post(cs.endpoint+"/createTask", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var createResult struct {
		ErrorID   int    `json:"errorId"`
		TaskID    string `json:"taskId"`
		ErrorDesc string `json:"errorDescription"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createResult); err != nil {
		return nil, err
	}
	if createResult.ErrorID != 0 {
		return nil, fmt.Errorf("capsolver error: %s", createResult.ErrorDesc)
	}

	// Poll
	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}

		pollBody, _ := json.Marshal(map[string]any{
			"clientKey": cs.apiKey,
			"taskId":    createResult.TaskID,
		})
		pollResp, err := cs.client.Post(cs.endpoint+"/getTaskResult", "application/json", strings.NewReader(string(pollBody)))
		if err != nil {
			continue
		}

		var result struct {
			Status   string `json:"status"`
			Solution struct {
				GRecaptchaResponse string `json:"gRecaptchaResponse"`
				Token              string `json:"token"`
			} `json:"solution"`
		}
		if err := json.NewDecoder(pollResp.Body).Decode(&result); err != nil {
			pollResp.Body.Close()
			continue
		}
		pollResp.Body.Close()

		if result.Status == "ready" {
			solution := result.Solution.GRecaptchaResponse
			if solution == "" {
				solution = result.Solution.Token
			}
			return &CAPTCHAResponse{
				Solution: solution,
				TaskID:   createResult.TaskID,
				Duration: time.Since(start),
			}, nil
		}
	}

	return nil, fmt.Errorf("capsolver timeout")
}

// DetectCAPTCHA checks a page for common CAPTCHA indicators.
func DetectCAPTCHA(html string) (CAPTCHAType, string) {
	htmlLower := strings.ToLower(html)

	// reCAPTCHA v2/v3
	if strings.Contains(htmlLower, "recaptcha") || strings.Contains(html, "g-recaptcha") {
		if siteKey := extractBetween(html, `data-sitekey="`, `"`); siteKey != "" {
			if strings.Contains(htmlLower, "recaptcha/api.js?render=") {
				return CAPTCHAReCaptchaV3, siteKey
			}
			return CAPTCHAReCaptchaV2, siteKey
		}
	}

	// hCaptcha
	if strings.Contains(htmlLower, "hcaptcha") || strings.Contains(html, "h-captcha") {
		if siteKey := extractBetween(html, `data-sitekey="`, `"`); siteKey != "" {
			return CAPTCHAHCaptcha, siteKey
		}
	}

	// Cloudflare Turnstile
	if strings.Contains(htmlLower, "turnstile") || strings.Contains(html, "cf-turnstile") {
		if siteKey := extractBetween(html, `data-sitekey="`, `"`); siteKey != "" {
			return CAPTCHATurnstile, siteKey
		}
	}

	return "", ""
}

// extractBetween extracts a substring between two delimiters.
func extractBetween(s, start, end string) string {
	idx := strings.Index(s, start)
	if idx < 0 {
		return ""
	}
	s = s[idx+len(start):]
	idx = strings.Index(s, end)
	if idx < 0 {
		return ""
	}
	return s[:idx]
}
