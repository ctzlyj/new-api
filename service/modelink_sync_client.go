package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	defaultModelinkAPIBaseURL     = "https://api.modelink.ai/v1"
	defaultModelinkMarketplaceURL = "https://modelink.ai/zh-CN/models"
	modelinkCatalogResponseLimit  = 10 << 20
)

type ModelinkSyncClient struct {
	httpClient     *http.Client
	apiBaseURL     string
	marketplaceURL string
}

func normalizeModelinkPricingRange(values []float64) []float64 {
	normalized := append([]float64(nil), values...)
	if len(normalized) == 2 && normalized[1] == -1 {
		normalized[1] = qiniuOpenEndedRange
	}
	return normalized
}

type modelinkMarketplaceModel struct {
	ID                  string                       `json:"id"`
	Name                string                       `json:"name"`
	Description         map[string]string            `json:"description"`
	Issuer              QiniuIssuer                  `json:"issuer"`
	IconURL             string                       `json:"iconUrl"`
	Features            []string                     `json:"features"`
	Tags                []string                     `json:"tags"`
	InputModalities     []string                     `json:"inputModalities"`
	OutputModalities    []string                     `json:"outputModalities"`
	SupportAPIProtocols []string                     `json:"supportApiProtocols"`
	RetirementAt        string                       `json:"retirementAt"`
	PricingRules        []modelinkMarketplacePricing `json:"pricingRules"`
}

type modelinkMarketplacePricing struct {
	InputRange  []float64                 `json:"inputRange"`
	OutputRange []float64                 `json:"outputRange"`
	Items       []modelinkMarketplaceItem `json:"items"`
}

type modelinkMarketplaceItem struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	CNYUnitPrice float64 `json:"cnyUnitPrice"`
	USDUnitPrice float64 `json:"usdUnitPrice"`
	UnitName     string  `json:"unitName"`
	UnitSize     float64 `json:"unitSize"`
}

func NewModelinkSyncClient(httpClient *http.Client, apiBaseURL, marketplaceURL string) *ModelinkSyncClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *httpClient
	previousCheckRedirect := clientCopy.CheckRedirect
	clientCopy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 && !strings.EqualFold(request.URL.Host, via[0].URL.Host) {
			return errors.New("modelink catalog redirect changed host")
		}
		if previousCheckRedirect != nil {
			return previousCheckRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	if strings.TrimSpace(apiBaseURL) == "" {
		apiBaseURL = defaultModelinkAPIBaseURL
	}
	if strings.TrimSpace(marketplaceURL) == "" {
		marketplaceURL = defaultModelinkMarketplaceURL
	}
	return &ModelinkSyncClient{
		httpClient:     &clientCopy,
		apiBaseURL:     strings.TrimRight(apiBaseURL, "/"),
		marketplaceURL: marketplaceURL,
	}
}

func (c *ModelinkSyncClient) FetchCallableModelIDs(ctx context.Context, apiKey string) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("modelink API key is empty")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create modelink models request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")
	body, err := c.fetch(request)
	if err != nil {
		return nil, fmt.Errorf("fetch modelink callable models: %w", err)
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := common.DecodeJson(bytes.NewReader(body), &response); err != nil {
		return nil, fmt.Errorf("decode modelink callable models: %w", err)
	}
	unique := make(map[string]struct{}, len(response.Data))
	for _, item := range response.Data {
		if modelID := strings.TrimSpace(item.ID); modelID != "" {
			unique[modelID] = struct{}{}
		}
	}
	modelIDs := make([]string, 0, len(unique))
	for modelID := range unique {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)
	return modelIDs, nil
}

func (c *ModelinkSyncClient) FetchMarketplaceModels(ctx context.Context) ([]QiniuMarketplaceModel, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.marketplaceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create modelink marketplace request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	body, err := c.fetch(request)
	if err != nil {
		return nil, fmt.Errorf("fetch modelink marketplace: %w", err)
	}
	rawModels, err := decodeModelinkMarketplaceModels(body)
	if err != nil {
		return nil, err
	}
	models := make([]QiniuMarketplaceModel, 0, len(rawModels))
	seen := make(map[string]struct{}, len(rawModels))
	for _, rawModel := range rawModels {
		modelID := strings.TrimSpace(rawModel.ID)
		if modelID == "" {
			return nil, errors.New("modelink marketplace contains an empty model ID")
		}
		if _, exists := seen[modelID]; exists {
			return nil, fmt.Errorf("modelink marketplace contains duplicate model ID %q", modelID)
		}
		seen[modelID] = struct{}{}
		pricingRules := make([]QiniuPricingRule, 0, len(rawModel.PricingRules))
		for _, rawRule := range rawModel.PricingRules {
			details := make(map[string]QiniuPrice, len(rawRule.Items))
			for _, item := range rawRule.Items {
				key := strings.TrimSpace(item.Key)
				if key == "" {
					continue
				}
				details[key] = QiniuPrice{
					UnitName:     item.UnitName,
					UnitSize:     item.UnitSize,
					UnitPriceCNY: item.CNYUnitPrice,
					UnitPriceUSD: item.USDUnitPrice,
					Name:         item.Label,
				}
			}
			pricingRules = append(pricingRules, QiniuPricingRule{
				InputRange:  normalizeModelinkPricingRange(rawRule.InputRange),
				OutputRange: normalizeModelinkPricingRange(rawRule.OutputRange),
				DetailsV2:   details,
			})
		}
		description := strings.TrimSpace(rawModel.Description["zh-CN"])
		if description == "" {
			description = strings.TrimSpace(rawModel.Description["en-US"])
		}
		models = append(models, QiniuMarketplaceModel{
			ModelID:          modelID,
			ID:               modelID,
			Name:             strings.TrimSpace(rawModel.Name),
			Description:      description,
			InputModalities:  rawModel.InputModalities,
			OutputModalities: rawModel.OutputModalities,
			Protocols:        rawModel.SupportAPIProtocols,
			Avatar:           strings.TrimSpace(rawModel.IconURL),
			Issuer:           rawModel.Issuer,
			Features:         normalizeModelinkLabels(rawModel.Features, modelinkFeatureLabels),
			HotTags:          normalizeModelinkLabels(rawModel.Tags, modelinkTagLabels),
			RetirementAt:     strings.TrimSpace(rawModel.RetirementAt),
			PricingRules:     pricingRules,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ModelID < models[j].ModelID })
	return models, nil
}

var modelinkFeatureLabels = map[string]string{
	"ai_programming":      "AI 编程",
	"deep_thinking":       "深度思考",
	"image_understanding": "图片理解",
	"tool_calling":        "工具调用",
	"video_understanding": "视频理解",
}

var modelinkTagLabels = map[string]string{
	"hot":          "热门",
	"limited_free": "限时免费",
}

func normalizeModelinkLabels(values []string, labels map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if label := labels[strings.ToLower(trimmed)]; label != "" {
			result = append(result, label)
		} else {
			result = append(result, trimmed)
		}
	}
	return result
}

func decodeModelinkMarketplaceModels(body []byte) ([]modelinkMarketplaceModel, error) {
	const prefix = `self.__next_f.push([1,`
	remaining := string(body)
	for {
		prefixIndex := strings.Index(remaining, prefix)
		if prefixIndex < 0 {
			break
		}
		remaining = remaining[prefixIndex+len(prefix):]
		literal, consumed, ok := modelinkJSONStringLiteral(remaining)
		if !ok {
			continue
		}
		remaining = remaining[consumed:]
		var chunk string
		if err := common.Unmarshal([]byte(literal), &chunk); err != nil {
			continue
		}
		models, found, err := decodeModelinkModelsFromChunk(chunk)
		if err != nil {
			return nil, fmt.Errorf("decode modelink marketplace metadata: %w", err)
		}
		if found {
			return models, nil
		}
	}
	return nil, errors.New("modelink marketplace models are missing")
}

func modelinkJSONStringLiteral(value string) (string, int, bool) {
	if len(value) == 0 || value[0] != '"' {
		return "", 0, false
	}
	escaped := false
	for index := 1; index < len(value); index++ {
		if escaped {
			escaped = false
			continue
		}
		switch value[index] {
		case '\\':
			escaped = true
		case '"':
			return value[:index+1], index + 1, true
		}
	}
	return "", 0, false
}

func decodeModelinkModelsFromChunk(chunk string) ([]modelinkMarketplaceModel, bool, error) {
	const marker = `"models":`
	remaining := chunk
	for {
		markerIndex := strings.Index(remaining, marker)
		if markerIndex < 0 {
			return nil, false, nil
		}
		remaining = strings.TrimLeft(remaining[markerIndex+len(marker):], " \t\r\n")
		if len(remaining) == 0 || remaining[0] != '[' {
			continue
		}
		array, ok := modelinkJSONArray(remaining)
		if !ok {
			return nil, false, errors.New("modelink marketplace models array is incomplete")
		}
		var models []modelinkMarketplaceModel
		if err := common.Unmarshal([]byte(array), &models); err != nil {
			return nil, false, err
		}
		return models, true, nil
	}
}

func modelinkJSONArray(value string) (string, bool) {
	depth := 0
	inString := false
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return value[:index+1], true
			}
		}
	}
	return "", false
}

func (c *ModelinkSyncClient) fetch(request *http.Request) ([]byte, error) {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, modelinkCatalogResponseLimit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > modelinkCatalogResponseLimit {
		return nil, errors.New("modelink catalog response is too large")
	}
	return body, nil
}
