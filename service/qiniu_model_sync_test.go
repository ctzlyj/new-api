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
		0.05,
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

func TestBuildQiniuCandidateSnapshotSortsModels(t *testing.T) {
	models := []QiniuMarketplaceModel{
		qiniuPricingModel("z-model", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)})),
		qiniuPricingModel("a-model", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)})),
	}

	snapshot, err := BuildQiniuCandidateSnapshot([]string{"z-model", "a-model"}, models, 0.05, time.Now())

	require.NoError(t, err)
	assert.Equal(t, []string{"a-model", "z-model"}, snapshot.ActiveModelIDs())
}

func TestBuildQiniuCandidateSnapshotRejectsDuplicateMarketplaceIDs(t *testing.T) {
	marketplaceModel := qiniuPricingModel("duplicate", qiniuRule(0, 99999999, 0, 99999999, map[string]QiniuPrice{"input": qiniuTokenPrice(0.001)}))

	_, err := BuildQiniuCandidateSnapshot([]string{"duplicate"}, []QiniuMarketplaceModel{marketplaceModel, marketplaceModel}, 0.05, time.Now())

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

	_, err := synchronizer.Sync(context.Background(), channel, QiniuSyncConfig{Markup: 0.05, ManagedTag: "qiniu-managed"})

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

	summary, err := synchronizer.Sync(context.Background(), channel, QiniuSyncConfig{Markup: 0.05, ManagedTag: "qiniu-managed"})

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
	_, err := synchronizer.Sync(context.Background(), &model.Channel{Status: common.ChannelStatusEnabled}, QiniuSyncConfig{ManagedTag: "qiniu-managed"})
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

	_, err := synchronizer.Sync(context.Background(), channel, QiniuSyncConfig{Markup: 0.05, ManagedTag: "qiniu-managed"})

	require.Error(t, err)
	assert.False(t, applied)
}
