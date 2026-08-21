package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	defaultQiniuAPIBaseURL     = "https://api.qnaigc.com/v1"
	defaultQiniuMarketplaceURL = "https://www.qiniu.com/ai/models"
	qiniuCatalogResponseLimit  = 10 << 20
)

var qiniuNextDataPattern = regexp.MustCompile(`(?is)<script[^>]*\bid=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)

type QiniuSyncClient struct {
	httpClient     *http.Client
	apiBaseURL     string
	marketplaceURL string
}

type QiniuPrice struct {
	UnitName     string  `json:"unit_name"`
	UnitSize     float64 `json:"unit_size"`
	UnitPriceCNY float64 `json:"unit_price"`
	UnitPriceUSD float64 `json:"unit_price_usd"`
	Name         string  `json:"name"`
}

type QiniuPricingRule struct {
	InputRange     []float64             `json:"input_range"`
	OutputRange    []float64             `json:"output_range"`
	InputItemType  string                `json:"input_item_type"`
	OutputItemType string                `json:"output_item_type"`
	DetailsV2      map[string]QiniuPrice `json:"details_v2"`
}

type QiniuModelArchitecture struct {
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

type QiniuIssuer struct {
	Name      string `json:"name"`
	Avatar    string `json:"avatar"`
	ModelPage string `json:"model_page"`
}

type QiniuMarketplaceModel struct {
	ModelID            string                 `json:"model_id"`
	ID                 string                 `json:"id"`
	Name               string                 `json:"name"`
	Description        string                 `json:"description"`
	Architecture       QiniuModelArchitecture `json:"architecture"`
	InputModalities    []string               `json:"input_modalities"`
	OutputModalities   []string               `json:"output_modalities"`
	Protocols          []string               `json:"protocols"`
	SupportedProtocols []string               `json:"support_api_protocols"`
	Avatar             string                 `json:"avatar"`
	Issuer             QiniuIssuer            `json:"issuer"`
	Features           []string               `json:"features"`
	HotTags            []string               `json:"hot_tags"`
	RetirementAt       string                 `json:"retirement_at"`
	PricingRules       []QiniuPricingRule     `json:"pricing_rules_v2"`
}

func NewQiniuSyncClient(httpClient *http.Client, apiBaseURL, marketplaceURL string) *QiniuSyncClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *httpClient
	previousCheckRedirect := clientCopy.CheckRedirect
	clientCopy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 && !strings.EqualFold(request.URL.Host, via[0].URL.Host) {
			return errors.New("qiniu catalog redirect changed host")
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
		apiBaseURL = defaultQiniuAPIBaseURL
	}
	if strings.TrimSpace(marketplaceURL) == "" {
		marketplaceURL = defaultQiniuMarketplaceURL
	}
	return &QiniuSyncClient{
		httpClient:     &clientCopy,
		apiBaseURL:     strings.TrimRight(apiBaseURL, "/"),
		marketplaceURL: marketplaceURL,
	}
}

func (c *QiniuSyncClient) FetchCallableModelIDs(ctx context.Context, apiKey string) ([]string, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("qiniu API key is empty")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBaseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create qiniu models request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Accept", "application/json")

	body, err := c.fetch(request)
	if err != nil {
		return nil, fmt.Errorf("fetch qiniu callable models: %w", err)
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := common.DecodeJson(bytes.NewReader(body), &response); err != nil {
		return nil, fmt.Errorf("decode qiniu callable models: %w", err)
	}
	unique := make(map[string]struct{}, len(response.Data))
	for _, item := range response.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID != "" {
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

func (c *QiniuSyncClient) FetchMarketplaceModels(ctx context.Context) ([]QiniuMarketplaceModel, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.marketplaceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create qiniu marketplace request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	body, err := c.fetch(request)
	if err != nil {
		return nil, fmt.Errorf("fetch qiniu marketplace: %w", err)
	}
	matches := qiniuNextDataPattern.FindSubmatch(body)
	if len(matches) != 2 {
		return nil, errors.New("qiniu marketplace __NEXT_DATA__ is missing")
	}
	var nextData struct {
		Props struct {
			PageProps struct {
				Models *[]QiniuMarketplaceModel `json:"models"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := common.DecodeJson(bytes.NewReader(matches[1]), &nextData); err != nil {
		return nil, fmt.Errorf("decode qiniu marketplace metadata: %w", err)
	}
	if nextData.Props.PageProps.Models == nil {
		return nil, errors.New("qiniu marketplace models are missing")
	}
	models := *nextData.Props.PageProps.Models
	for index := range models {
		if models[index].ModelID == "" {
			models[index].ModelID = models[index].ID
		}
		if len(models[index].InputModalities) == 0 {
			models[index].InputModalities = models[index].Architecture.InputModalities
		}
		if len(models[index].OutputModalities) == 0 {
			models[index].OutputModalities = models[index].Architecture.OutputModalities
		}
		if len(models[index].Protocols) == 0 {
			models[index].Protocols = models[index].SupportedProtocols
		}
	}
	return models, nil
}

func (c *QiniuSyncClient) fetch(request *http.Request) ([]byte, error) {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, qiniuCatalogResponseLimit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > qiniuCatalogResponseLimit {
		return nil, errors.New("qiniu catalog response is too large")
	}
	return body, nil
}
