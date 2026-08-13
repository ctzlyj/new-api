package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"gorm.io/gorm"
)

const (
	qiniuBillingModeOptionKey = "billing_setting.billing_mode"
	qiniuBillingExprOptionKey = "billing_setting.billing_expr"
)

type QiniuManagedModel struct {
	ID          string
	Name        string
	Description string
	BillingExpr string
	Endpoints   []constant.EndpointType
}

type QiniuCandidateSnapshot struct {
	Models   []QiniuManagedModel
	Rejected map[string]QiniuAdmissionReason
}

type QiniuSyncSummary struct {
	Callable int `json:"callable"`
	Public   int `json:"public"`
	Accepted int `json:"accepted"`
	Hidden   int `json:"hidden"`
	Added    int `json:"added"`
	Updated  int `json:"updated"`
	Removed  int `json:"removed"`
}

type QiniuApplyOptions struct {
	ManagedTag string
}

func (snapshot QiniuCandidateSnapshot) ActiveModelIDs() []string {
	modelIDs := make([]string, len(snapshot.Models))
	for index, managedModel := range snapshot.Models {
		modelIDs[index] = managedModel.ID
	}
	return modelIDs
}

func BuildQiniuCandidateSnapshot(callableIDs []string, marketplaceModels []QiniuMarketplaceModel, markup float64, now time.Time) (QiniuCandidateSnapshot, error) {
	marketplaceByID := make(map[string]QiniuMarketplaceModel, len(marketplaceModels))
	for _, marketplaceModel := range marketplaceModels {
		modelID := strings.TrimSpace(marketplaceModel.ModelID)
		if modelID == "" {
			return QiniuCandidateSnapshot{}, fmt.Errorf("qiniu marketplace contains an empty model ID")
		}
		if _, exists := marketplaceByID[modelID]; exists {
			return QiniuCandidateSnapshot{}, fmt.Errorf("qiniu marketplace contains duplicate model ID %q", modelID)
		}
		marketplaceByID[modelID] = marketplaceModel
	}

	callable := make(map[string]struct{}, len(callableIDs))
	for _, callableID := range callableIDs {
		modelID := strings.TrimSpace(callableID)
		if modelID != "" {
			callable[modelID] = struct{}{}
		}
	}
	sortedCallable := make([]string, 0, len(callable))
	for modelID := range callable {
		sortedCallable = append(sortedCallable, modelID)
	}
	sort.Strings(sortedCallable)

	snapshot := QiniuCandidateSnapshot{
		Models:   make([]QiniuManagedModel, 0, len(sortedCallable)),
		Rejected: make(map[string]QiniuAdmissionReason),
	}
	for _, modelID := range sortedCallable {
		marketplaceModel, exists := marketplaceByID[modelID]
		if !exists {
			snapshot.Rejected[modelID] = QiniuAdmissionMissingMetadata
			continue
		}
		expression, reason, err := AdmitQiniuModel(marketplaceModel, callable, now, markup)
		if reason != QiniuAdmissionAccepted {
			snapshot.Rejected[modelID] = reason
			continue
		}
		if err != nil {
			return QiniuCandidateSnapshot{}, fmt.Errorf("admit qiniu model %q: %w", modelID, err)
		}
		name := strings.TrimSpace(marketplaceModel.Name)
		if name == "" {
			name = modelID
		}
		snapshot.Models = append(snapshot.Models, QiniuManagedModel{
			ID:          modelID,
			Name:        name,
			Description: strings.TrimSpace(marketplaceModel.Description),
			BillingExpr: expression,
			Endpoints:   []constant.EndpointType{constant.EndpointTypeOpenAI},
		})
	}
	return snapshot, nil
}

func ApplyQiniuCandidateSnapshot(ctx context.Context, snapshot QiniuCandidateSnapshot, options QiniuApplyOptions) (QiniuSyncSummary, error) {
	managedTag := strings.TrimSpace(options.ManagedTag)
	if managedTag == "" {
		return QiniuSyncSummary{}, errors.New("qiniu managed channel tag is empty")
	}
	activeIDs := snapshot.ActiveModelIDs()
	if len(activeIDs) == 0 {
		return QiniuSyncSummary{}, errors.New("qiniu candidate snapshot is empty")
	}
	if !sort.StringsAreSorted(activeIDs) {
		return QiniuSyncSummary{}, errors.New("qiniu candidate models are not sorted")
	}
	for _, managedModel := range snapshot.Models {
		if managedModel.ID == "" {
			return QiniuSyncSummary{}, errors.New("qiniu candidate contains an empty model ID")
		}
		if err := billing_setting.SmokeTestExpr(managedModel.BillingExpr); err != nil {
			return QiniuSyncSummary{}, fmt.Errorf("qiniu model %q billing expression is invalid: %w", managedModel.ID, err)
		}
	}

	var channel model.Channel
	var previousActiveIDs []string
	var optionValues map[string]string
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var channels []model.Channel
		if err := tx.Where("tag = ?", managedTag).Limit(2).Find(&channels).Error; err != nil {
			return err
		}
		if len(channels) != 1 {
			return fmt.Errorf("expected one qiniu managed channel with tag %q, found %d", managedTag, len(channels))
		}
		channel = channels[0]
		if channel.Status != common.ChannelStatusEnabled {
			return errors.New("qiniu managed channel is disabled")
		}
		previousActiveIDs = splitQiniuModelIDs(channel.Models)

		var previouslyManagedIDs []string
		if err := tx.Model(&model.Model{}).Where("managed_by = ?", managedTag).Pluck("model_name", &previouslyManagedIDs).Error; err != nil {
			return err
		}

		channel.Models = strings.Join(activeIDs, ",")
		if err := tx.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("models", channel.Models).Error; err != nil {
			return err
		}
		if err := channel.UpdateAbilities(tx); err != nil {
			return err
		}

		now := common.GetTimestamp()
		for _, managedModel := range snapshot.Models {
			endpoints, err := common.Marshal(managedModel.Endpoints)
			if err != nil {
				return fmt.Errorf("marshal endpoints for %q: %w", managedModel.ID, err)
			}
			var existing model.Model
			err = tx.Where("model_name = ?", managedModel.ID).First(&existing).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				existing = model.Model{
					ModelName:    managedModel.ID,
					Description:  managedModel.Description,
					Tags:         "Qiniu",
					Endpoints:    string(endpoints),
					Status:       1,
					SyncOfficial: 0,
					ManagedBy:    managedTag,
					CreatedTime:  now,
					UpdatedTime:  now,
				}
				if err := tx.Create(&existing).Error; err != nil {
					return err
				}
				if err := tx.Model(&model.Model{}).Where("id = ?", existing.Id).Updates(map[string]any{"sync_official": 0, "managed_by": managedTag}).Error; err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if existing.ManagedBy != managedTag {
					return fmt.Errorf("model %q is not owned by qiniu sync", managedModel.ID)
				}
				if err := tx.Model(&model.Model{}).Where("id = ?", existing.Id).Updates(map[string]any{
					"description":   managedModel.Description,
					"tags":          "Qiniu",
					"endpoints":     string(endpoints),
					"status":        1,
					"sync_official": 0,
					"managed_by":    managedTag,
					"updated_time":  now,
				}).Error; err != nil {
					return err
				}
			}
		}

		hideQuery := tx.Model(&model.Model{}).Where("managed_by = ?", managedTag)
		if len(activeIDs) > 0 {
			hideQuery = hideQuery.Where("model_name NOT IN ?", activeIDs)
		}
		if err := hideQuery.Updates(map[string]any{"status": 0, "updated_time": now}).Error; err != nil {
			return err
		}

		billingModes, billingExpressions, err := loadQiniuBillingOptionsTx(tx)
		if err != nil {
			return err
		}
		managedBillingIDs := make(map[string]struct{}, len(previouslyManagedIDs)+len(previousActiveIDs))
		for _, modelID := range append(previouslyManagedIDs, previousActiveIDs...) {
			managedBillingIDs[modelID] = struct{}{}
		}
		for modelID := range managedBillingIDs {
			delete(billingModes, modelID)
			delete(billingExpressions, modelID)
		}
		for _, managedModel := range snapshot.Models {
			billingModes[managedModel.ID] = billing_setting.BillingModeTieredExpr
			billingExpressions[managedModel.ID] = managedModel.BillingExpr
		}
		modeJSON, err := common.Marshal(billingModes)
		if err != nil {
			return err
		}
		expressionJSON, err := common.Marshal(billingExpressions)
		if err != nil {
			return err
		}
		optionValues = map[string]string{
			qiniuBillingModeOptionKey: string(modeJSON),
			qiniuBillingExprOptionKey: string(expressionJSON),
		}
		return model.UpdateOptionsTx(tx, optionValues)
	})
	if err != nil {
		return QiniuSyncSummary{}, err
	}
	if err := model.ApplyOptionUpdates(optionValues); err != nil {
		return QiniuSyncSummary{}, fmt.Errorf("reload qiniu billing options: %w", err)
	}
	model.CacheUpdateChannel(&channel)
	model.InvalidatePricingCache()

	previousSet := make(map[string]struct{}, len(previousActiveIDs))
	for _, modelID := range previousActiveIDs {
		previousSet[modelID] = struct{}{}
	}
	activeSet := make(map[string]struct{}, len(activeIDs))
	for _, modelID := range activeIDs {
		activeSet[modelID] = struct{}{}
	}
	summary := QiniuSyncSummary{Accepted: len(activeIDs), Hidden: len(snapshot.Rejected)}
	for modelID := range activeSet {
		if _, exists := previousSet[modelID]; exists {
			summary.Updated++
		} else {
			summary.Added++
		}
	}
	for modelID := range previousSet {
		if _, exists := activeSet[modelID]; !exists {
			summary.Removed++
		}
	}
	return summary, nil
}

func loadQiniuBillingOptionsTx(tx *gorm.DB) (map[string]string, map[string]string, error) {
	modes := billing_setting.GetBillingModeCopy()
	expressions := billing_setting.GetBillingExprCopy()
	for key, target := range map[string]*map[string]string{
		qiniuBillingModeOptionKey: &modes,
		qiniuBillingExprOptionKey: &expressions,
	} {
		var option model.Option
		err := tx.Where("key = ?", key).First(&option).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		decoded := make(map[string]string)
		if err := common.Unmarshal([]byte(option.Value), &decoded); err != nil {
			return nil, nil, fmt.Errorf("decode %s: %w", key, err)
		}
		*target = decoded
	}
	return modes, expressions, nil
}

func splitQiniuModelIDs(models string) []string {
	seen := make(map[string]struct{})
	for _, modelID := range strings.Split(models, ",") {
		modelID = strings.TrimSpace(modelID)
		if modelID != "" {
			seen[modelID] = struct{}{}
		}
	}
	modelIDs := make([]string, 0, len(seen))
	for modelID := range seen {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)
	return modelIDs
}

type QiniuCatalog interface {
	FetchCallableModelIDs(context.Context, string) ([]string, error)
	FetchMarketplaceModels(context.Context) ([]QiniuMarketplaceModel, error)
}

type QiniuSyncConfig struct {
	Markup     float64
	ManagedTag string
}

type QiniuModelSynchronizer struct {
	Catalog QiniuCatalog
	Now     func() time.Time
	Apply   func(context.Context, QiniuCandidateSnapshot, QiniuApplyOptions) (QiniuSyncSummary, error)
}

func (s QiniuModelSynchronizer) Sync(ctx context.Context, channel *model.Channel, config QiniuSyncConfig) (QiniuSyncSummary, error) {
	if channel == nil {
		return QiniuSyncSummary{}, errors.New("qiniu managed channel is missing")
	}
	if channel.Status != common.ChannelStatusEnabled {
		return QiniuSyncSummary{}, errors.New("qiniu managed channel is disabled")
	}
	if strings.TrimSpace(channel.Key) == "" {
		return QiniuSyncSummary{}, errors.New("qiniu managed channel API key is empty")
	}
	if s.Catalog == nil {
		return QiniuSyncSummary{}, errors.New("qiniu catalog client is missing")
	}
	callableIDs, err := s.Catalog.FetchCallableModelIDs(ctx, channel.Key)
	if err != nil {
		return QiniuSyncSummary{}, fmt.Errorf("fetch qiniu callable model catalog: %w", err)
	}
	marketplaceModels, err := s.Catalog.FetchMarketplaceModels(ctx)
	if err != nil {
		return QiniuSyncSummary{}, fmt.Errorf("fetch qiniu marketplace metadata: %w", err)
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	snapshot, err := BuildQiniuCandidateSnapshot(callableIDs, marketplaceModels, config.Markup, now)
	if err != nil {
		return QiniuSyncSummary{}, fmt.Errorf("build qiniu candidate snapshot: %w", err)
	}
	if len(callableIDs) == 0 || len(snapshot.Models) == 0 {
		return QiniuSyncSummary{}, fmt.Errorf("refusing to apply empty qiniu snapshot: callable=%d accepted=%d", len(callableIDs), len(snapshot.Models))
	}
	apply := s.Apply
	if apply == nil {
		apply = ApplyQiniuCandidateSnapshot
	}
	summary, err := apply(ctx, snapshot, QiniuApplyOptions{ManagedTag: config.ManagedTag})
	if err != nil {
		return QiniuSyncSummary{}, fmt.Errorf("apply qiniu candidate snapshot: %w", err)
	}
	summary.Callable = len(callableIDs)
	summary.Public = len(marketplaceModels)
	summary.Accepted = len(snapshot.Models)
	summary.Hidden = len(snapshot.Rejected)
	return summary, nil
}
