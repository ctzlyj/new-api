package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildQiniuCandidateSnapshotUsesCallablePricedTextIntersection(t *testing.T) {
	priced := qiniuPricingModel("text-a", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"input": qiniuTokenPrice(0.001), "output": qiniuTokenPrice(0.002),
	}))
	priced.Name = "Text A"
	priced.Description = "A text model"
	missingPrice := QiniuMarketplaceModel{ModelID: "missing-price", Protocols: []string{"openai"}, OutputModalities: []string{"text"}}

	snapshot, err := BuildQiniuCandidateSnapshot(
		[]string{"text-a", "missing-price", "not-public"},
		[]QiniuMarketplaceModel{priced, missingPrice},
		qiniuResourcePackagePricing(70),
		time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC),
	)

	require.NoError(t, err)
	assert.Equal(t, []string{"text-a"}, snapshot.ActiveModelIDs())
	assert.Equal(t, QiniuAdmissionMissingPrice, snapshot.Rejected["missing-price"])
	assert.Equal(t, QiniuAdmissionMissingMetadata, snapshot.Rejected["not-public"])
	require.Len(t, snapshot.Models, 1)
	assert.Equal(t, "Text A", snapshot.Models[0].Name)
	assert.Equal(t, []constant.EndpointType{constant.EndpointTypeOpenAI}, snapshot.Models[0].Endpoints)
}

func TestBuildQiniuCandidateSnapshotIncludesCatalogMetadata(t *testing.T) {
	marketplaceModel := qiniuPricingModel("qwen3-235b-a22b", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"nth_input": qiniuTokenPrice(0.002), "th_output": qiniuTokenPrice(0.020),
	}))
	marketplaceModel.Avatar = "https://static.qiniu.com/ai-inference/model-icons/qwen.png"
	marketplaceModel.Issuer = QiniuIssuer{Name: "Aliyun"}
	marketplaceModel.Features = []string{"工具调用", "深度思考"}
	marketplaceModel.HotTags = []string{"热门"}
	marketplaceModel.InputModalities = []string{"text", "image"}
	marketplaceModel.RetirementAt = "2026-09-01T00:00:00Z"

	snapshot, err := BuildQiniuCandidateSnapshot(
		[]string{marketplaceModel.ModelID},
		[]QiniuMarketplaceModel{marketplaceModel},
		qiniuResourcePackagePricing(70),
		time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC),
	)

	require.NoError(t, err)
	require.Len(t, snapshot.Models, 1)
	managedModel := snapshot.Models[0]
	assert.Equal(t, marketplaceModel.Avatar, managedModel.Icon)
	assert.Equal(t, "Aliyun", managedModel.VendorName)
	assert.Equal(t, marketplaceModel.Avatar, managedModel.VendorIcon)
	assert.Equal(t, "Qiniu,工具调用,深度思考,热门,文本输入,图片输入,文本输出", managedModel.Tags)
}

func TestBuildQiniuCandidateSnapshotRejectsRetiredModel(t *testing.T) {
	marketplaceModel := qiniuPricingModel("retired-model", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
		"input": qiniuTokenPrice(0.001),
	}))
	marketplaceModel.RetirementAt = "2026-08-01T00:00:00Z"

	snapshot, err := BuildQiniuCandidateSnapshot(
		[]string{marketplaceModel.ModelID},
		[]QiniuMarketplaceModel{marketplaceModel},
		qiniuResourcePackagePricing(70),
		time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC),
	)

	require.NoError(t, err)
	assert.Empty(t, snapshot.ActiveModelIDs())
	assert.Equal(t, QiniuAdmissionRetired, snapshot.Rejected[marketplaceModel.ModelID])
}

func TestBuildQiniuCandidateSnapshotSortsModels(t *testing.T) {
	models := []QiniuMarketplaceModel{
		qiniuPricingModel("z-model", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)})),
		qiniuPricingModel("a-model", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)})),
	}

	snapshot, err := BuildQiniuCandidateSnapshot([]string{"z-model", "a-model"}, models, qiniuResourcePackagePricing(70), time.Now())

	require.NoError(t, err)
	assert.Equal(t, []string{"a-model", "z-model"}, snapshot.ActiveModelIDs())
}

func TestBuildQiniuCandidateSnapshotRejectsDuplicateMarketplaceIDs(t *testing.T) {
	marketplaceModel := qiniuPricingModel("duplicate", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}))

	_, err := BuildQiniuCandidateSnapshot([]string{"duplicate"}, []QiniuMarketplaceModel{marketplaceModel, marketplaceModel}, qiniuResourcePackagePricing(70), time.Now())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

type fakeQiniuCatalog struct {
	callable       []string
	marketplace    []QiniuMarketplaceModel
	callableErr    error
	marketplaceErr error
	receivedKey    string
}

func (catalog *fakeQiniuCatalog) FetchCallableModelIDs(_ context.Context, apiKey string) ([]string, error) {
	catalog.receivedKey = apiKey
	return catalog.callable, catalog.callableErr
}

func (catalog *fakeQiniuCatalog) FetchMarketplaceModels(context.Context) ([]QiniuMarketplaceModel, error) {
	return catalog.marketplace, catalog.marketplaceErr
}

func TestQiniuModelSynchronizerKeepsSnapshotWhenMarketplaceFetchFails(t *testing.T) {
	applied := false
	catalog := &fakeQiniuCatalog{callable: []string{"text-a"}, marketplaceErr: errors.New("offline")}
	synchronizer := QiniuModelSynchronizer{
		Catalog: catalog,
		Apply: func(context.Context, QiniuCandidateSnapshot, QiniuApplyOptions) (QiniuSyncSummary, error) {
			applied = true
			return QiniuSyncSummary{}, nil
		},
	}
	channel := &model.Channel{Key: "test-key", Status: common.ChannelStatusEnabled}

	_, err := synchronizer.Sync(context.Background(), channel, QiniuSyncConfig{Pricing: qiniuResourcePackagePricing(70), ManagedTag: "qiniu-managed"})

	require.Error(t, err)
	assert.False(t, applied)
	assert.NotContains(t, err.Error(), "test-key")
}

func TestQiniuModelSynchronizerBuildsAndAppliesSnapshot(t *testing.T) {
	marketplaceModel := qiniuPricingModel("text-a", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}))
	catalog := &fakeQiniuCatalog{callable: []string{"text-a", "missing"}, marketplace: []QiniuMarketplaceModel{marketplaceModel}}
	var applied QiniuCandidateSnapshot
	synchronizer := QiniuModelSynchronizer{
		Catalog: catalog,
		Now:     func() time.Time { return time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) },
		Apply: func(_ context.Context, snapshot QiniuCandidateSnapshot, _ QiniuApplyOptions) (QiniuSyncSummary, error) {
			applied = snapshot
			return QiniuSyncSummary{Added: 1}, nil
		},
	}
	channel := &model.Channel{Key: "test-key", Status: common.ChannelStatusEnabled}

	summary, err := synchronizer.Sync(context.Background(), channel, QiniuSyncConfig{Pricing: qiniuResourcePackagePricing(70), ManagedTag: "qiniu-managed"})

	require.NoError(t, err)
	assert.Equal(t, "test-key", catalog.receivedKey)
	assert.Equal(t, []string{"text-a"}, applied.ActiveModelIDs())
	assert.Equal(t, 2, summary.Callable)
	assert.Equal(t, 1, summary.Public)
	assert.Equal(t, 1, summary.Accepted)
	assert.Equal(t, 1, summary.Hidden)
	assert.Equal(t, 1, summary.Added)
}

func TestQiniuModelSynchronizerRejectsEmptyKey(t *testing.T) {
	synchronizer := QiniuModelSynchronizer{Catalog: &fakeQiniuCatalog{}}
	_, err := synchronizer.Sync(context.Background(), &model.Channel{Status: common.ChannelStatusEnabled}, QiniuSyncConfig{Pricing: qiniuResourcePackagePricing(70), ManagedTag: "qiniu-managed"})
	require.Error(t, err)
}

func TestQiniuModelSynchronizerRejectsEmptyCandidateWithoutApplying(t *testing.T) {
	applied := false
	synchronizer := QiniuModelSynchronizer{
		Catalog: &fakeQiniuCatalog{},
		Apply: func(context.Context, QiniuCandidateSnapshot, QiniuApplyOptions) (QiniuSyncSummary, error) {
			applied = true
			return QiniuSyncSummary{}, nil
		},
	}
	channel := &model.Channel{Key: "test-key", Status: common.ChannelStatusEnabled}

	_, err := synchronizer.Sync(context.Background(), channel, QiniuSyncConfig{Pricing: qiniuResourcePackagePricing(70), ManagedTag: "qiniu-managed"})

	require.Error(t, err)
	assert.False(t, applied)
}

func TestQiniuModelSynchronizerBuildsPrioritySourceRoutes(t *testing.T) {
	priced := func(modelID string) QiniuMarketplaceModel {
		return qiniuPricingModel(modelID, qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
			"input": qiniuTokenPrice(0.004),
		}))
	}
	qiniuCatalog := &fakeQiniuCatalog{
		callable:    []string{"shared", "qiniu-only"},
		marketplace: []QiniuMarketplaceModel{priced("shared"), priced("qiniu-only")},
	}
	modelinkCatalog := &fakeQiniuCatalog{
		callable: []string{"shared", "modelink-only", "modelink-unpriced"},
		marketplace: []QiniuMarketplaceModel{
			priced("shared"),
			priced("modelink-only"),
			{ModelID: "modelink-unpriced", Protocols: []string{"openai"}, OutputModalities: []string{"text"}},
		},
	}
	var applied QiniuCandidateSnapshot
	var options QiniuApplyOptions
	synchronizer := QiniuModelSynchronizer{
		Now: func() time.Time { return time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC) },
		Apply: func(_ context.Context, snapshot QiniuCandidateSnapshot, applyOptions QiniuApplyOptions) (QiniuSyncSummary, error) {
			applied = snapshot
			options = applyOptions
			return QiniuSyncSummary{}, nil
		},
	}
	sources := []QiniuSyncSource{
		{Name: "qiniu", SourceTag: "Qiniu", ManagedTag: "qiniu-managed", Channel: &model.Channel{Key: "same-key", Status: common.ChannelStatusEnabled}, Catalog: qiniuCatalog},
		{Name: "modelink", SourceTag: "Modelink", ManagedTag: "modelink-managed", Channel: &model.Channel{Key: "same-key", Status: common.ChannelStatusEnabled}, Catalog: modelinkCatalog},
	}

	summary, err := synchronizer.SyncSources(context.Background(), sources, QiniuSyncConfig{Pricing: qiniuResourcePackagePricing(70), ManagedTag: "qiniu-managed"})

	require.NoError(t, err)
	assert.Equal(t, []string{"modelink-only", "qiniu-only", "shared"}, applied.ActiveModelIDs())
	require.Len(t, options.Routes, 2)
	assert.Equal(t, QiniuChannelRoute{ManagedTag: "qiniu-managed", ModelIDs: []string{"qiniu-only", "shared"}}, options.Routes[0])
	assert.Equal(t, QiniuChannelRoute{ManagedTag: "modelink-managed", ModelIDs: []string{"modelink-only"}}, options.Routes[1])
	assert.Equal(t, 4, summary.Callable)
	assert.Equal(t, 3, summary.Accepted)
	assert.Equal(t, 1, summary.Hidden)
	for _, managedModel := range applied.Models {
		if managedModel.ID == "modelink-only" {
			assert.Contains(t, managedModel.Tags, "Modelink")
		}
		if managedModel.ID == "shared" {
			assert.Contains(t, managedModel.Tags, "Qiniu")
			assert.NotContains(t, managedModel.Tags, "Modelink")
		}
	}
}

func TestQiniuModelSynchronizerExcludesRetiredModelsFromBothRoutes(t *testing.T) {
	priced := func(modelID string) QiniuMarketplaceModel {
		return qiniuPricingModel(modelID, qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{
			"input": qiniuTokenPrice(0.004),
		}))
	}
	retired := func(modelID string) QiniuMarketplaceModel {
		model := priced(modelID)
		model.RetirementAt = "2026-08-01T00:00:00Z"
		return model
	}
	qiniuCatalog := &fakeQiniuCatalog{
		callable:    []string{"qiniu-active", "qiniu-retired"},
		marketplace: []QiniuMarketplaceModel{priced("qiniu-active"), retired("qiniu-retired")},
	}
	modelinkCatalog := &fakeQiniuCatalog{
		callable:    []string{"modelink-active", "modelink-retired", "openai/gpt-5.2-chat"},
		marketplace: []QiniuMarketplaceModel{priced("modelink-active"), retired("modelink-retired"), priced("openai/gpt-5.2-chat")},
	}
	var applied QiniuCandidateSnapshot
	var options QiniuApplyOptions
	synchronizer := QiniuModelSynchronizer{
		Now: func() time.Time { return time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC) },
		Apply: func(_ context.Context, snapshot QiniuCandidateSnapshot, applyOptions QiniuApplyOptions) (QiniuSyncSummary, error) {
			applied = snapshot
			options = applyOptions
			return QiniuSyncSummary{}, nil
		},
	}
	sources := []QiniuSyncSource{
		{Name: "qiniu", SourceTag: "Qiniu", ManagedTag: "qiniu-managed", Channel: &model.Channel{Key: "same-key", Status: common.ChannelStatusEnabled}, Catalog: qiniuCatalog},
		{Name: "modelink", SourceTag: "Modelink", ManagedTag: "modelink-managed", Channel: &model.Channel{Key: "same-key", Status: common.ChannelStatusEnabled}, Catalog: modelinkCatalog},
	}

	summary, err := synchronizer.SyncSources(context.Background(), sources, QiniuSyncConfig{Pricing: qiniuResourcePackagePricing(70), ManagedTag: "qiniu-managed"})

	require.NoError(t, err)
	assert.Equal(t, []string{"modelink-active", "qiniu-active"}, applied.ActiveModelIDs())
	assert.Equal(t, QiniuAdmissionRetired, applied.Rejected["qiniu-retired"])
	assert.Equal(t, QiniuAdmissionRetired, applied.Rejected["modelink-retired"])
	assert.Equal(t, QiniuAdmissionDisabled, applied.Rejected["openai/gpt-5.2-chat"])
	require.Len(t, options.Routes, 2)
	assert.Equal(t, QiniuChannelRoute{ManagedTag: "qiniu-managed", ModelIDs: []string{"qiniu-active"}}, options.Routes[0])
	assert.Equal(t, QiniuChannelRoute{ManagedTag: "modelink-managed", ModelIDs: []string{"modelink-active"}}, options.Routes[1])
	assert.Equal(t, 5, summary.Callable)
	assert.Equal(t, 2, summary.Accepted)
	assert.Equal(t, 3, summary.Hidden)
}
