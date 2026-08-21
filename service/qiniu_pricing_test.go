package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func qiniuTokenPrice(unitPriceCNY float64) QiniuPrice {
	return QiniuPrice{UnitName: "token", UnitSize: 1000, UnitPriceCNY: unitPriceCNY}
}

func qiniuResourcePackagePricing(displayPointsPerQuotaUnit float64) QiniuResourcePackagePricing {
	return QiniuResourcePackagePricing{
		CostCNYPer100MTokens:      323,
		SaleCNYPer100MTokens:      350,
		PointsPerCNY:              20,
		DisplayPointsPerQuotaUnit: displayPointsPerQuotaUnit,
	}
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

	expr, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 0.5 + c * 2)`, expr)
}

func TestBuildQiniuBillingExprCacheAndNonCache(t *testing.T) {
	model := qiniuPricingModel("model-a", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"ncache": qiniuTokenPrice(0.004),
		"cache":  qiniuTokenPrice(0.001),
		"output": qiniuTokenPrice(0.010),
	}))

	expr, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 1 + c * 2.5 + cr * 0.25)`, expr)
}

func TestBuildQiniuBillingExprPeakOffpeak(t *testing.T) {
	t.Run("uses peak prices", func(t *testing.T) {
		model := qiniuPricingModel("deepseek/deepseek-v4-flash-20260731", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
			"cache_offpeak":  qiniuTokenPrice(0.00005),
			"cache_peak":     qiniuTokenPrice(0.0001),
			"ncache_offpeak": qiniuTokenPrice(0.0015),
			"ncache_peak":    qiniuTokenPrice(0.003),
			"output_offpeak": qiniuTokenPrice(0.0045),
			"output_peak":    qiniuTokenPrice(0.009),
		}))
		callable := map[string]struct{}{model.ModelID: {}}

		expr, reason, err := AdmitQiniuModel(model, callable, time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC), qiniuResourcePackagePricing(70))

		require.NoError(t, err)
		assert.Equal(t, QiniuAdmissionAccepted, reason)
		assert.Equal(t, `v1:tier("qiniu_0", p * 0.75 + c * 2.25 + cr * 0.025)`, expr)
	})

	t.Run("rejects an offpeak meter without its peak counterpart", func(t *testing.T) {
		model := qiniuPricingModel("incomplete-peak-pricing", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
			"ncache_peak":    qiniuTokenPrice(0.003),
			"ncache_offpeak": qiniuTokenPrice(0.0015),
			"output_offpeak": qiniuTokenPrice(0.0045),
		}))

		_, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))

		require.Error(t, err)
		assert.Contains(t, err.Error(), `meter "output_offpeak" is missing peak counterpart "output_peak"`)
	})
}

func TestBuildQiniuBillingExprUsesHigherThinkingPrice(t *testing.T) {
	model := qiniuPricingModel("qwen3-235b-a22b", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"nth_input":  qiniuTokenPrice(0.002),
		"th_input":   qiniuTokenPrice(0.002),
		"nth_output": qiniuTokenPrice(0.008),
		"th_output":  qiniuTokenPrice(0.020),
	}))

	expr, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 0.5 + c * 5)`, expr)
}

func TestBuildQiniuBillingExprInputAndOutputTiers(t *testing.T) {
	model := qiniuPricingModel("tiered",
		qiniuRule(0, 32000, 0, 200, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001), "output": qiniuTokenPrice(0.002)}),
		qiniuRule(0, 32000, 200, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001), "output": qiniuTokenPrice(0.008)}),
		qiniuRule(32000, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.002), "output": qiniuTokenPrice(0.010)}),
	)

	expr, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))

	require.NoError(t, err)
	assert.Contains(t, expr, "len < 32000")
	assert.Contains(t, expr, "c < 200")
	assert.Contains(t, expr, `tier("qiniu_2", p * 0.5 + c * 2.5)`)
}

func TestBuildQiniuBillingExprAllowsFreeModel(t *testing.T) {
	model := qiniuPricingModel("free", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"input":  qiniuTokenPrice(0),
		"output": qiniuTokenPrice(0),
	}))

	expr, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 0 + c * 0)`, expr)
}

func TestBuildQiniuBillingExprAllowsFiniteContextDomain(t *testing.T) {
	model := qiniuPricingModel("finite",
		qiniuRule(0, 32000, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}),
		qiniuRule(32000, 256000, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.002)}),
	)

	expr, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))

	require.NoError(t, err)
	assert.Contains(t, expr, "len < 32000")
	assert.Contains(t, expr, `tier("qiniu_1", p * 0.5)`)
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

	expr, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 1 + c * 2.5 + cr * 0.25)`, expr)
}
func TestBuildQiniuBillingExprConvertsPointsToInternalQuotaCurrency(t *testing.T) {
	model := qiniuPricingModel("baseline", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"input": qiniuTokenPrice(0.004),
	}))

	expr, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(68))

	require.NoError(t, err)
	assert.Equal(t, `v1:tier("qiniu_0", p * 1.0294117647058822)`, expr)
}

func TestBuildQiniuBillingExprRejectsGrossMarginBelowFivePercent(t *testing.T) {
	model := qiniuPricingModel("unsafe-margin", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"input": qiniuTokenPrice(0.004),
	}))
	pricing := qiniuResourcePackagePricing(68)
	pricing.SaleCNYPer100MTokens = 339

	_, err := BuildQiniuBillingExpr(model, pricing)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gross margin")
}

func TestBuildQiniuBillingExprRejectsUnsupportedUnitsAndMeters(t *testing.T) {
	t.Run("unit", func(t *testing.T) {
		model := qiniuPricingModel("seconds", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
			"input": {UnitName: "second", UnitSize: 1, UnitPriceCNY: 1},
		}))
		_, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))
		require.Error(t, err)
	})
	t.Run("meter", func(t *testing.T) {
		model := qiniuPricingModel("media", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
			"req": {UnitName: "request", UnitSize: 1, UnitPriceCNY: 1},
		}))
		_, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))
		require.Error(t, err)
	})
}

func TestBuildQiniuBillingExprRejectsRangeGap(t *testing.T) {
	model := qiniuPricingModel("gap",
		qiniuRule(0, 32000, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}),
		qiniuRule(64000, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.002)}),
	)
	_, err := BuildQiniuBillingExpr(model, qiniuResourcePackagePricing(70))
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
		{name: "retired but callable", model: func() QiniuMarketplaceModel {
			model := qiniuPricingModel("model-a", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}))
			model.RetirementAt = "2026-08-01T00:00:00Z"
			return model
		}(), reason: QiniuAdmissionAccepted},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, reason, _ := AdmitQiniuModel(test.model, callable, now, qiniuResourcePackagePricing(70))
			assert.Equal(t, test.reason, reason)
		})
	}
}
