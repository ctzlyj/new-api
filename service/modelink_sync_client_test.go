package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modelinkFlightPage(t *testing.T, models string) string {
	t.Helper()
	chunk := `e:["$","$L19",null,{"locale":"zh-CN","models":` + models + `}]`
	encoded, err := common.Marshal(chunk)
	require.NoError(t, err)
	return `<html><body><script>self.__next_f.push([1,"translation models"])</script><script>self.__next_f.push([1,` + string(encoded) + `])</script></body></html>`
}

func TestModelinkSyncClientFetchMarketplaceModelsNormalizesFlightPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/zh-CN/models", request.URL.Path)
		_, _ = io.WriteString(response, modelinkFlightPage(t, `[{
			"id":"anthropic/claude-sonnet-5",
			"name":"Claude-Sonnet-5",
			"description":{"zh-CN":"中文说明","en-US":"English description"},
			"issuer":{"key":"anthropic","name":"Anthropic"},
			"iconUrl":"https://static.qiniu.com/ai-inference/model-icons/anthropic.png",
			"features":["tool_calling","deep_thinking"],
			"tags":["hot"],
			"inputModalities":["text","image"],
			"outputModalities":["text"],
			"supportApiProtocols":["openai","anthropic"],
			"retirementAt":"2027-04-16",
			"pricingRules":[{
				"inputRange":[0,-1],
				"outputRange":[0,-1],
				"items":[
					{"key":"ncache","label":"非缓存输入","cnyUnitPrice":0.006,"unitName":"token","unitSize":1000},
					{"key":"output","label":"输出","cnyUnitPrice":0.024,"unitName":"token","unitSize":1000}
				]
			}]
		}]`))
	}))
	defer server.Close()

	client := NewModelinkSyncClient(server.Client(), server.URL, server.URL+"/zh-CN/models")
	models, err := client.FetchMarketplaceModels(context.Background())

	require.NoError(t, err)
	require.Len(t, models, 1)
	model := models[0]
	assert.Equal(t, "anthropic/claude-sonnet-5", model.ModelID)
	assert.Equal(t, "Claude-Sonnet-5", model.Name)
	assert.Equal(t, "中文说明", model.Description)
	assert.Equal(t, "Anthropic", model.Issuer.Name)
	assert.Equal(t, "https://static.qiniu.com/ai-inference/model-icons/anthropic.png", model.Avatar)
	assert.Equal(t, []string{"工具调用", "深度思考"}, model.Features)
	assert.Equal(t, []string{"热门"}, model.HotTags)
	assert.Equal(t, []string{"text", "image"}, model.InputModalities)
	assert.Equal(t, []string{"text"}, model.OutputModalities)
	assert.Equal(t, []string{"openai", "anthropic"}, model.Protocols)
	assert.Equal(t, "2027-04-16", model.RetirementAt)
	require.Len(t, model.PricingRules, 1)
	assert.Equal(t, []float64{0, qiniuOpenEndedRange}, model.PricingRules[0].InputRange)
	assert.Equal(t, []float64{0, qiniuOpenEndedRange}, model.PricingRules[0].OutputRange)
	assert.Equal(t, 0.006, model.PricingRules[0].DetailsV2["ncache"].UnitPriceCNY)
	assert.Equal(t, 0.024, model.PricingRules[0].DetailsV2["output"].UnitPriceCNY)
}

func TestModelinkSyncClientFetchMarketplaceModelsRejectsDuplicateIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, modelinkFlightPage(t, `[{"id":"duplicate","pricingRules":[]},{"id":"duplicate","pricingRules":[]}]`))
	}))
	defer server.Close()

	client := NewModelinkSyncClient(server.Client(), server.URL, server.URL)
	_, err := client.FetchMarketplaceModels(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestModelinkSyncClientFetchMarketplaceModelsRejectsMissingPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, `<html><body><script>self.__next_f.push([1,"no catalog"])</script></body></html>`)
	}))
	defer server.Close()

	client := NewModelinkSyncClient(server.Client(), server.URL, server.URL)
	_, err := client.FetchMarketplaceModels(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "models")
}

func TestModelinkSyncClientFetchCallableModelIDsSortsAndSanitizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "Bearer test-key", request.Header.Get("Authorization"))
		_, _ = io.WriteString(response, `{"data":[{"id":"z-model"},{"id":"a-model"},{"id":"a-model"}]}`)
	}))
	defer server.Close()

	client := NewModelinkSyncClient(server.Client(), server.URL, server.URL)
	models, err := client.FetchCallableModelIDs(context.Background(), "test-key")

	require.NoError(t, err)
	assert.Equal(t, []string{"a-model", "z-model"}, models)

	_, err = client.FetchCallableModelIDs(context.Background(), "")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "test-key"))
}

func TestModelinkSyncClientRejectsCrossHostRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, `{"data":[]}`)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusFound)
	}))
	defer source.Close()

	client := NewModelinkSyncClient(source.Client(), source.URL, source.URL)
	_, err := client.FetchCallableModelIDs(context.Background(), "test-key")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirect")
}
