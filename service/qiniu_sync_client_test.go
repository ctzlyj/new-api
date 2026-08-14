package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQiniuSyncClientFetchCallableModelIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/models", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"data":[{"id":"model-b"},{"id":"model-a"},{"id":"model-a"},{"id":""}]}`)
	}))
	defer server.Close()

	client := NewQiniuSyncClient(server.Client(), server.URL, server.URL)
	modelIDs, err := client.FetchCallableModelIDs(context.Background(), "test-key")

	require.NoError(t, err)
	assert.Equal(t, []string{"model-a", "model-b"}, modelIDs)
}

func TestQiniuSyncClientFetchCallableModelIDsSanitizesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad token"}`)
	}))
	defer server.Close()

	client := NewQiniuSyncClient(server.Client(), server.URL, server.URL)
	_, err := client.FetchCallableModelIDs(context.Background(), "secret-value")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.NotContains(t, err.Error(), "secret-value")
}

func TestQiniuSyncClientFetchMarketplaceModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><body><script id="ignored">{}</script><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"models":[{"model_id":"model-a","name":"Model A","pricing_rules_v2":[]}]}}}</script></body></html>`)
	}))
	defer server.Close()

	client := NewQiniuSyncClient(server.Client(), server.URL, server.URL)
	models, err := client.FetchMarketplaceModels(context.Background())

	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "model-a", models[0].ModelID)
	assert.Equal(t, "Model A", models[0].Name)
}

func TestQiniuSyncClientFetchMarketplaceModelsNormalizesLiveSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<script id="__NEXT_DATA__">{"props":{"pageProps":{"models":[{"id":"deepseek/model","architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"support_api_protocols":["openai"],"retirement_at":"","pricing_rules_v2":[{"input_range":[0,99999999],"output_range":[0,99999999],"details_v2":{"ncache":{"unit_name":"token","unit_size":1000,"unit_price":0.004,"unit_price_usd":0.001},"output":{"unit_name":"token","unit_size":1000,"unit_price":0.008,"unit_price_usd":0.002}}}]}]}}}</script>`)
	}))
	defer server.Close()

	client := NewQiniuSyncClient(server.Client(), server.URL, server.URL)
	models, err := client.FetchMarketplaceModels(context.Background())

	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "deepseek/model", models[0].ModelID)
	assert.Equal(t, []string{"text", "image"}, models[0].InputModalities)
	assert.Equal(t, []string{"text"}, models[0].OutputModalities)
	assert.Equal(t, []string{"openai"}, models[0].Protocols)
	require.Len(t, models[0].PricingRules, 1)
	assert.Equal(t, 0.004, models[0].PricingRules[0].DetailsV2["ncache"].UnitPriceCNY)
	assert.Equal(t, 0.001, models[0].PricingRules[0].DetailsV2["ncache"].UnitPriceUSD)
}
func TestQiniuSyncClientFetchMarketplaceModelsRejectsMissingData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><body><script id="__NEXT_DATA__">{"props":{"pageProps":{}}}</script></body></html>`)
	}))
	defer server.Close()

	client := NewQiniuSyncClient(server.Client(), server.URL, server.URL)
	_, err := client.FetchMarketplaceModels(context.Background())

	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "models")
}

func TestQiniuSyncClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", qiniuCatalogResponseLimit+1))
	}))
	defer server.Close()

	client := NewQiniuSyncClient(server.Client(), server.URL, server.URL)
	_, err := client.FetchMarketplaceModels(context.Background())

	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "large")
}
