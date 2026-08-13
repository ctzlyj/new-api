package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func qiniuTokenPrice(unitPriceUSD float64) QiniuPrice {
	return QiniuPrice{UnitName: "token", UnitSize: 1000, UnitPriceUSD: unitPriceUSD}
}

func qiniuPricingModel(modelID string, rules ...QiniuPricingRule) QiniuMarketplaceModel {
	return QiniuMarketplaceModel{
		ModelID:          modelID,
		Protocols:        []string{"openai"},
		OutputModalities: []string{"text"},
		PricingRules:     rules,
	}
}

func qiniuRule(inputMin, inputMax, outputMin, outputMax float64, details map[string]QiniuPrice) QiniuPricingRule {
	return QiniuPricingRule{
		InputRange:  []float64{inputMin, inputMax},
		OutputRange: []float64{outputMin, outputMax},
		DetailsV2:   details,
	}
}

func TestBuildQiniuBillingExprFlatInputOutput(t *testing.T) {
	model := qiniuPricingModel("model-a", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"input":  qiniuTokenPrice(0.002),
		"output": qiniuTokenPrice(0.008),
	}))

	expr, err := BuildQiniuBillingExpr(model, 0.05)

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 2.1 + c * 8.4)`, expr)
}

func TestBuildQiniuBillingExprCacheAndNonCache(t *testing.T) {
	model := qiniuPricingModel("model-a", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"ncache": qiniuTokenPrice(0.004),
		"cache":  qiniuTokenPrice(0.001),
		"output": qiniuTokenPrice(0.010),
	}))

	expr, err := BuildQiniuBillingExpr(model, 0.05)

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 4.2 + c * 10.5 + cr * 1.05)`, expr)
}

func TestBuildQiniuBillingExprInputAndOutputTiers(t *testing.T) {
	model := qiniuPricingModel("tiered",
		qiniuRule(0, 32000, 0, 200, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001), "output": qiniuTokenPrice(0.002)}),
		qiniuRule(0, 32000, 200, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001), "output": qiniuTokenPrice(0.008)}),
		qiniuRule(32000, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.002), "output": qiniuTokenPrice(0.010)}),
	)

	expr, err := BuildQiniuBillingExpr(model, 0.05)

	require.NoError(t, err)
	assert.Contains(t, expr, "len < 32000")
	assert.Contains(t, expr, "c < 200")
	assert.Contains(t, expr, `tier("qiniu_2", p * 2.1 + c * 10.5)`)
}

func TestBuildQiniuBillingExprAllowsFreeModel(t *testing.T) {
	model := qiniuPricingModel("free", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"input":  qiniuTokenPrice(0),
		"output": qiniuTokenPrice(0),
	}))

	expr, err := BuildQiniuBillingExpr(model, 0.05)

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 0 + c * 0)`, expr)
}

func TestBuildQiniuBillingExprAllowsFiniteContextDomain(t *testing.T) {
	model := qiniuPricingModel("finite",
		qiniuRule(0, 32000, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}),
		qiniuRule(32000, 256000, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.002)}),
	)

	expr, err := BuildQiniuBillingExpr(model, 0.05)

	require.NoError(t, err)
	assert.Contains(t, expr, "len < 32000")
	assert.Contains(t, expr, `tier("qiniu_1", p * 2.1)`)
}

func TestBuildQiniuBillingExprIgnoresNonRealtimeAlternativePrices(t *testing.T) {
	model := qiniuPricingModel("alternatives", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"ncache":    qiniuTokenPrice(0.004),
		"cache":     qiniuTokenPrice(0.001),
		"output":    qiniuTokenPrice(0.010),
		"bi_input":  qiniuTokenPrice(0.002),
		"bi_output": qiniuTokenPrice(0.005),
		"ex_cache":  qiniuTokenPrice(0.0005),
		"c_cache":   qiniuTokenPrice(0.006),
	}))

	expr, err := BuildQiniuBillingExpr(model, 0.05)

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 4.2 + c * 10.5 + cr * 1.05)`, expr)
}
func TestBuildQiniuBillingExprRejectsUnsupportedUnitsAndMeters(t *testing.T) {
	t.Run("unit", func(t *testing.T) {
		model := qiniuPricingModel("seconds", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
			"input": {UnitName: "second", UnitSize: 1, UnitPriceUSD: 1},
		}))
		_, err := BuildQiniuBillingExpr(model, 0.05)
		require.Error(t, err)
	})
	t.Run("meter", func(t *testing.T) {
		model := qiniuPricingModel("media", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
			"req": {UnitName: "request", UnitSize: 1, UnitPriceUSD: 1},
		}))
		_, err := BuildQiniuBillingExpr(model, 0.05)
		require.Error(t, err)
	})
}

func TestBuildQiniuBillingExprRejectsRangeGap(t *testing.T) {
	model := qiniuPricingModel("gap",
		qiniuRule(0, 32000, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}),
		qiniuRule(64000, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.002)}),
	)
	_, err := BuildQiniuBillingExpr(model, 0.05)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gap")
}

func TestAdmitQiniuModel(t *testing.T) {
	callable := map[string]struct{}{"model-a": {}}
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		model  QiniuMarketplaceModel
		reason QiniuAdmissionReason
	}{
		{name: "accepted", model: qiniuPricingModel("model-a", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)})), reason: QiniuAdmissionAccepted},
		{name: "not callable", model: qiniuPricingModel("model-b", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)})), reason: QiniuAdmissionNotCallable},
		{name: "not openai", model: QiniuMarketplaceModel{ModelID: "model-a", Protocols: []string{"anthropic"}, OutputModalities: []string{"text"}, PricingRules: []QiniuPricingRule{qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)})}}, reason: QiniuAdmissionNotOpenAI},
		{name: "image output", model: QiniuMarketplaceModel{ModelID: "model-a", Protocols: []string{"openai"}, OutputModalities: []string{"image"}, PricingRules: []QiniuPricingRule{qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)})}}, reason: QiniuAdmissionNotTextOutput},
		{name: "missing price", model: QiniuMarketplaceModel{ModelID: "model-a", Protocols: []string{"openai"}, OutputModalities: []string{"text"}}, reason: QiniuAdmissionMissingPrice},
		{name: "retired", model: func() QiniuMarketplaceModel {
			model := qiniuPricingModel("model-a", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}))
			model.RetirementAt = "2026-08-01T00:00:00Z"
			return model
		}(), reason: QiniuAdmissionRetired},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, reason, _ := AdmitQiniuModel(test.model, callable, now, 0.05)
			assert.Equal(t, test.reason, reason)
		})
	}
}
