package service

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/setting/billing_setting"
)

const (
	qiniuOpenEndedRange                  = 99999999
	qiniuBaselineCNYPer1KDeductionTokens = 0.004
	qiniuMinimumGrossMargin              = 0.05
)

type QiniuResourcePackagePricing struct {
	CostCNYPer100MTokens      float64
	SaleCNYPer100MTokens      float64
	PointsPerCNY              float64
	DisplayPointsPerQuotaUnit float64
}

func (pricing QiniuResourcePackagePricing) validate() error {
	values := map[string]float64{
		"cost per 100M tokens":          pricing.CostCNYPer100MTokens,
		"sale price per 100M tokens":    pricing.SaleCNYPer100MTokens,
		"points per CNY":                pricing.PointsPerCNY,
		"display points per quota unit": pricing.DisplayPointsPerQuotaUnit,
	}
	for name, value := range values {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("qiniu resource package %s is invalid", name)
		}
	}
	grossMargin := (pricing.SaleCNYPer100MTokens - pricing.CostCNYPer100MTokens) / pricing.SaleCNYPer100MTokens
	if grossMargin < qiniuMinimumGrossMargin {
		return fmt.Errorf("qiniu resource package gross margin %.4f is below %.4f", grossMargin, qiniuMinimumGrossMargin)
	}
	return nil
}

func (pricing QiniuResourcePackagePricing) coefficientPerDeductionRatio() float64 {
	pointsPer100MTokens := pricing.SaleCNYPer100MTokens * pricing.PointsPerCNY
	pointsPer1MTokens := pointsPer100MTokens / 100
	return pointsPer1MTokens / pricing.DisplayPointsPerQuotaUnit
}

type QiniuAdmissionReason string

const (
	QiniuAdmissionAccepted        QiniuAdmissionReason = "accepted"
	QiniuAdmissionNotCallable     QiniuAdmissionReason = "not_callable"
	QiniuAdmissionNotOpenAI       QiniuAdmissionReason = "not_openai_compatible"
	QiniuAdmissionNotTextOutput   QiniuAdmissionReason = "not_text_output"
	QiniuAdmissionRetired         QiniuAdmissionReason = "retired"
	QiniuAdmissionMissingPrice    QiniuAdmissionReason = "missing_price"
	QiniuAdmissionMissingMetadata QiniuAdmissionReason = "missing_metadata"
	QiniuAdmissionInvalidPricing  QiniuAdmissionReason = "invalid_pricing"
)

type qiniuValidatedRule struct {
	inputMin  float64
	inputMax  float64
	outputMin float64
	outputMax float64
	cost      string
}

func AdmitQiniuModel(model QiniuMarketplaceModel, callable map[string]struct{}, _ time.Time, pricing QiniuResourcePackagePricing) (string, QiniuAdmissionReason, error) {
	if _, ok := callable[model.ModelID]; !ok {
		return "", QiniuAdmissionNotCallable, nil
	}
	if !containsFold(model.Protocols, "openai") {
		return "", QiniuAdmissionNotOpenAI, nil
	}
	if !containsFold(model.OutputModalities, "text") {
		return "", QiniuAdmissionNotTextOutput, nil
	}
	if len(model.PricingRules) == 0 {
		return "", QiniuAdmissionMissingPrice, nil
	}
	expr, err := BuildQiniuBillingExpr(model, pricing)
	if err != nil {
		return "", QiniuAdmissionInvalidPricing, err
	}
	return expr, QiniuAdmissionAccepted, nil
}

func BuildQiniuBillingExpr(model QiniuMarketplaceModel, pricing QiniuResourcePackagePricing) (string, error) {
	if len(model.PricingRules) == 0 {
		return "", errors.New("qiniu pricing rules are empty")
	}
	if err := pricing.validate(); err != nil {
		return "", err
	}

	rules := make([]qiniuValidatedRule, 0, len(model.PricingRules))
	for index, rule := range model.PricingRules {
		validated, err := validateQiniuPricingRule(rule, pricing)
		if err != nil {
			return "", fmt.Errorf("qiniu pricing rule %d: %w", index, err)
		}
		rules = append(rules, validated)
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].inputMin != rules[j].inputMin {
			return rules[i].inputMin < rules[j].inputMin
		}
		if rules[i].outputMin != rules[j].outputMin {
			return rules[i].outputMin < rules[j].outputMin
		}
		if rules[i].inputMax != rules[j].inputMax {
			return rules[i].inputMax < rules[j].inputMax
		}
		return rules[i].outputMax < rules[j].outputMax
	})
	if err := validateQiniuPricingCoverage(rules); err != nil {
		return "", err
	}

	tiers := make([]string, len(rules))
	for index, rule := range rules {
		tiers[index] = fmt.Sprintf("tier(\"qiniu_%d\", %s)", index, rule.cost)
	}
	expression := tiers[len(tiers)-1]
	for index := len(tiers) - 2; index >= 0; index-- {
		condition := qiniuRuleCondition(rules[index])
		expression = fmt.Sprintf("%s ? %s : %s", condition, tiers[index], expression)
	}
	expression = "v1:" + expression
	if err := billing_setting.SmokeTestExpr(expression); err != nil {
		return "", fmt.Errorf("qiniu billing expression smoke test failed: %w", err)
	}
	return expression, nil
}

func validateQiniuPricingRule(rule QiniuPricingRule, pricing QiniuResourcePackagePricing) (qiniuValidatedRule, error) {
	if len(rule.InputRange) != 2 || len(rule.OutputRange) != 2 {
		return qiniuValidatedRule{}, errors.New("input and output ranges must contain two values")
	}
	validated := qiniuValidatedRule{
		inputMin:  rule.InputRange[0],
		inputMax:  rule.InputRange[1],
		outputMin: rule.OutputRange[0],
		outputMax: rule.OutputRange[1],
	}
	for _, value := range []float64{validated.inputMin, validated.inputMax, validated.outputMin, validated.outputMax} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return qiniuValidatedRule{}, errors.New("range contains an invalid value")
		}
	}
	if validated.inputMax <= validated.inputMin || validated.outputMax <= validated.outputMin {
		return qiniuValidatedRule{}, errors.New("range maximum must be greater than minimum")
	}
	if rule.InputItemType != "" || rule.OutputItemType != "" {
		return qiniuValidatedRule{}, errors.New("item-based pricing is not supported")
	}
	if len(rule.DetailsV2) == 0 {
		return qiniuValidatedRule{}, errors.New("pricing details are empty")
	}

	normalizedMeters := make(map[string]struct{}, len(rule.DetailsV2))
	for meter := range rule.DetailsV2 {
		normalizedMeters[strings.ToLower(strings.TrimSpace(meter))] = struct{}{}
	}

	variablePrices := make(map[string]float64, len(rule.DetailsV2))
	for meter, price := range rule.DetailsV2 {
		normalizedMeter := strings.ToLower(strings.TrimSpace(meter))
		if strings.HasSuffix(normalizedMeter, "_offpeak") && qiniuIgnoredPricingMeter(normalizedMeter) {
			peakMeter := strings.TrimSuffix(normalizedMeter, "_offpeak") + "_peak"
			if _, exists := normalizedMeters[peakMeter]; !exists {
				return qiniuValidatedRule{}, fmt.Errorf("meter %q is missing peak counterpart %q", meter, peakMeter)
			}
		}
		if qiniuIgnoredPricingMeter(meter) {
			continue
		}
		variable, ok := qiniuBillingVariable(meter)
		if !ok {
			return qiniuValidatedRule{}, fmt.Errorf("unsupported pricing meter %q", meter)
		}
		if !strings.EqualFold(price.UnitName, "token") || price.UnitSize <= 0 || math.IsNaN(price.UnitSize) || math.IsInf(price.UnitSize, 0) {
			return qiniuValidatedRule{}, fmt.Errorf("meter %q has unsupported unit", meter)
		}
		if price.UnitPriceCNY < 0 || math.IsNaN(price.UnitPriceCNY) || math.IsInf(price.UnitPriceCNY, 0) {
			return qiniuValidatedRule{}, fmt.Errorf("meter %q has invalid CNY price", meter)
		}
		priceCNYPer1KTokens := price.UnitPriceCNY * (1_000 / price.UnitSize)
		deductionRatio := priceCNYPer1KTokens / qiniuBaselineCNYPer1KDeductionTokens
		coefficient := deductionRatio * pricing.coefficientPerDeductionRatio()
		if existing, exists := variablePrices[variable]; exists {
			variablePrices[variable] = math.Max(existing, coefficient)
			continue
		}
		variablePrices[variable] = coefficient
	}

	variables := []string{"p", "c", "cr", "cc"}
	terms := make([]string, 0, len(variablePrices))
	for _, variable := range variables {
		if coefficient, ok := variablePrices[variable]; ok {
			terms = append(terms, variable+" * "+formatQiniuCoefficient(coefficient))
		}
	}
	if len(terms) == 0 {
		return qiniuValidatedRule{}, errors.New("pricing rule has no token meters")
	}
	validated.cost = strings.Join(terms, " + ")
	return validated, nil
}

func validateQiniuPricingCoverage(rules []qiniuValidatedRule) error {
	inputBounds := []float64{0}
	outputBounds := []float64{0}
	maxInput := float64(0)
	maxOutput := float64(0)
	for _, rule := range rules {
		inputBounds = append(inputBounds, rule.inputMin, rule.inputMax)
		outputBounds = append(outputBounds, rule.outputMin, rule.outputMax)
		maxInput = math.Max(maxInput, rule.inputMax)
		maxOutput = math.Max(maxOutput, rule.outputMax)
	}
	inputBounds = sortedUniqueQiniuBounds(inputBounds)
	outputBounds = sortedUniqueQiniuBounds(outputBounds)
	if inputBounds[0] != 0 || outputBounds[0] != 0 || maxInput <= 0 || maxOutput <= 0 {
		return errors.New("qiniu pricing ranges do not start at zero")
	}
	for inputIndex := 0; inputIndex < len(inputBounds)-1; inputIndex++ {
		if inputBounds[inputIndex] >= maxInput {
			break
		}
		inputStart := inputBounds[inputIndex]
		inputEnd := math.Min(inputBounds[inputIndex+1], maxInput)
		for outputIndex := 0; outputIndex < len(outputBounds)-1; outputIndex++ {
			if outputBounds[outputIndex] >= maxOutput {
				break
			}
			outputStart := outputBounds[outputIndex]
			outputEnd := math.Min(outputBounds[outputIndex+1], maxOutput)
			matches := 0
			for _, rule := range rules {
				if rule.inputMin <= inputStart && rule.inputMax >= inputEnd && rule.outputMin <= outputStart && rule.outputMax >= outputEnd {
					matches++
				}
			}
			if matches == 0 {
				return fmt.Errorf("qiniu pricing range gap at input %s and output %s", formatQiniuCoefficient(inputStart), formatQiniuCoefficient(outputStart))
			}
			if matches > 1 {
				return fmt.Errorf("qiniu pricing range overlap at input %s and output %s", formatQiniuCoefficient(inputStart), formatQiniuCoefficient(outputStart))
			}
		}
	}
	return nil
}
func sortedUniqueQiniuBounds(values []float64) []float64 {
	sort.Float64s(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func qiniuRuleCondition(rule qiniuValidatedRule) string {
	conditions := make([]string, 0, 4)
	if rule.inputMin > 0 {
		conditions = append(conditions, "len >= "+formatQiniuCoefficient(rule.inputMin))
	}
	if rule.inputMax < qiniuOpenEndedRange {
		conditions = append(conditions, "len < "+formatQiniuCoefficient(rule.inputMax))
	}
	if rule.outputMin > 0 {
		conditions = append(conditions, "c >= "+formatQiniuCoefficient(rule.outputMin))
	}
	if rule.outputMax < qiniuOpenEndedRange {
		conditions = append(conditions, "c < "+formatQiniuCoefficient(rule.outputMax))
	}
	if len(conditions) == 0 {
		return "true"
	}
	return strings.Join(conditions, " && ")
}

func qiniuBillingVariable(meter string) (string, bool) {
	normalizedMeter := strings.ToLower(strings.TrimSpace(meter))
	normalizedMeter = strings.TrimSuffix(normalizedMeter, "_peak")
	switch normalizedMeter {
	case "input", "ncache", "nth_input", "th_input":
		return "p", true
	case "output", "nth_output", "th_output":
		return "c", true
	case "cache":
		return "cr", true
	default:
		return "", false
	}
}

func qiniuIgnoredPricingMeter(meter string) bool {
	switch strings.ToLower(strings.TrimSpace(meter)) {
	case "bi_input", "bi_output", "ex_cache", "c_cache",
		"input_offpeak", "ncache_offpeak", "output_offpeak", "cache_offpeak":
		return true
	default:
		return false
	}
}
func formatQiniuCoefficient(value float64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func parseQiniuRetirementAt(value string) (time.Time, error) {
	formats := []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"}
	for _, format := range formats {
		parsed, err := time.Parse(format, value)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q", value)
}
