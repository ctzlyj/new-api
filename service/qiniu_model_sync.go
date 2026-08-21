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
	Icon        string
	Tags        string
	VendorName  string
	VendorIcon  string
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
	Routes     []QiniuChannelRoute
}

type QiniuChannelRoute struct {
	ManagedTag string
	ModelIDs   []string
}

func (snapshot QiniuCandidateSnapshot) ActiveModelIDs() []string {
	modelIDs := make([]string, len(snapshot.Models))
	for index, managedModel := range snapshot.Models {
		modelIDs[index] = managedModel.ID
	}
	return modelIDs
}

func BuildQiniuCandidateSnapshot(callableIDs []string, marketplaceModels []QiniuMarketplaceModel, pricing QiniuResourcePackagePricing, now time.Time) (QiniuCandidateSnapshot, error) {
	return buildQiniuCandidateSnapshot(callableIDs, marketplaceModels, pricing, now, "Qiniu")
}

func buildQiniuCandidateSnapshot(callableIDs []string, marketplaceModels []QiniuMarketplaceModel, pricing QiniuResourcePackagePricing, now time.Time, sourceTag string) (QiniuCandidateSnapshot, error) {
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
		expression, reason, err := AdmitQiniuModel(marketplaceModel, callable, now, pricing)
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
			Icon:        strings.TrimSpace(marketplaceModel.Avatar),
			Tags:        buildQiniuModelTags(marketplaceModel, now, sourceTag),
			VendorName:  strings.TrimSpace(marketplaceModel.Issuer.Name),
			VendorIcon:  strings.TrimSpace(marketplaceModel.Avatar),
			BillingExpr: expression,
			Endpoints:   []constant.EndpointType{constant.EndpointTypeOpenAI},
		})
	}
	return snapshot, nil
}

func buildQiniuModelTags(marketplaceModel QiniuMarketplaceModel, now time.Time, sourceTag string) string {
	tags := []string{sourceTag}
	tags = append(tags, marketplaceModel.Features...)
	tags = append(tags, marketplaceModel.HotTags...)
	modalityLabels := map[string]string{
		"text":  "文本",
		"image": "图片",
		"audio": "音频",
		"video": "视频",
		"file":  "文件",
	}
	for _, modality := range marketplaceModel.InputModalities {
		if label := modalityLabels[strings.ToLower(strings.TrimSpace(modality))]; label != "" {
			tags = append(tags, label+"输入")
		}
	}
	for _, modality := range marketplaceModel.OutputModalities {
		if label := modalityLabels[strings.ToLower(strings.TrimSpace(modality))]; label != "" {
			tags = append(tags, label+"输出")
		}
	}
	if marketplaceModel.RetirementAt != "" {
		if retirementAt, err := parseQiniuRetirementAt(marketplaceModel.RetirementAt); err == nil && !retirementAt.After(now) {
			tags = append(tags, "供应商已标记退役")
		}
	}

	seen := make(map[string]struct{}, len(tags))
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, tag)
	}
	return strings.Join(normalized, ",")
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
	activeSet := make(map[string]struct{}, len(activeIDs))
	for _, modelID := range activeIDs {
		activeSet[modelID] = struct{}{}
	}
	routes := options.Routes
	if len(routes) == 0 {
		routes = []QiniuChannelRoute{{ManagedTag: managedTag, ModelIDs: activeIDs}}
	}
	routeTags := make(map[string]struct{}, len(routes))
	routedIDs := make(map[string]struct{}, len(activeIDs))
	for index := range routes {
		routes[index].ManagedTag = strings.TrimSpace(routes[index].ManagedTag)
		if routes[index].ManagedTag == "" {
			return QiniuSyncSummary{}, errors.New("qiniu route channel tag is empty")
		}
		if _, exists := routeTags[routes[index].ManagedTag]; exists {
			return QiniuSyncSummary{}, fmt.Errorf("duplicate qiniu route channel tag %q", routes[index].ManagedTag)
		}
		routeTags[routes[index].ManagedTag] = struct{}{}
		if !sort.StringsAreSorted(routes[index].ModelIDs) {
			return QiniuSyncSummary{}, fmt.Errorf("qiniu route %q models are not sorted", routes[index].ManagedTag)
		}
		for _, modelID := range routes[index].ModelIDs {
			if _, exists := activeSet[modelID]; !exists {
				return QiniuSyncSummary{}, fmt.Errorf("qiniu route %q contains inactive model %q", routes[index].ManagedTag, modelID)
			}
			if _, exists := routedIDs[modelID]; exists {
				return QiniuSyncSummary{}, fmt.Errorf("qiniu model %q has duplicate routes", modelID)
			}
			routedIDs[modelID] = struct{}{}
		}
	}
	if len(routedIDs) != len(activeIDs) {
		return QiniuSyncSummary{}, errors.New("qiniu routes do not cover the active snapshot")
	}

	channels := make([]model.Channel, len(routes))
	var previousActiveIDs []string
	var optionValues map[string]string
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index, route := range routes {
			var matched []model.Channel
			if err := tx.Where("tag = ?", route.ManagedTag).Limit(2).Find(&matched).Error; err != nil {
				return err
			}
			if len(matched) != 1 {
				return fmt.Errorf("expected one qiniu managed channel with tag %q, found %d", route.ManagedTag, len(matched))
			}
			channels[index] = matched[0]
			if channels[index].Status != common.ChannelStatusEnabled {
				return fmt.Errorf("qiniu managed channel %q is disabled", route.ManagedTag)
			}
			previousActiveIDs = append(previousActiveIDs, splitQiniuModelIDs(channels[index].Models)...)
		}

		var previouslyManagedIDs []string
		if err := tx.Model(&model.Model{}).Where("managed_by = ?", managedTag).Pluck("model_name", &previouslyManagedIDs).Error; err != nil {
			return err
		}

		for index, route := range routes {
			channels[index].Models = strings.Join(route.ModelIDs, ",")
			if err := tx.Model(&model.Channel{}).Where("id = ?", channels[index].Id).Update("models", channels[index].Models).Error; err != nil {
				return err
			}
			if err := channels[index].UpdateAbilities(tx); err != nil {
				return err
			}
		}

		now := common.GetTimestamp()
		vendorIDs := make(map[string]int)
		for _, managedModel := range snapshot.Models {
			endpoints, err := common.Marshal(managedModel.Endpoints)
			if err != nil {
				return fmt.Errorf("marshal endpoints for %q: %w", managedModel.ID, err)
			}
			vendorID := 0
			if managedModel.VendorName != "" {
				if cachedVendorID, exists := vendorIDs[managedModel.VendorName]; exists {
					vendorID = cachedVendorID
				} else {
					var vendor model.Vendor
					err = tx.Where("name = ?", managedModel.VendorName).First(&vendor).Error
					switch {
					case errors.Is(err, gorm.ErrRecordNotFound):
						vendor = model.Vendor{
							Name:        managedModel.VendorName,
							Icon:        managedModel.VendorIcon,
							Status:      1,
							CreatedTime: now,
							UpdatedTime: now,
						}
						if err := tx.Create(&vendor).Error; err != nil {
							return err
						}
					case err != nil:
						return err
					default:
						updates := map[string]any{"status": 1, "updated_time": now}
						if managedModel.VendorIcon != "" {
							updates["icon"] = managedModel.VendorIcon
						}
						if err := tx.Model(&model.Vendor{}).Where("id = ?", vendor.Id).Updates(updates).Error; err != nil {
							return err
						}
					}
					vendorID = vendor.Id
					vendorIDs[managedModel.VendorName] = vendorID
				}
			}

			var existing model.Model
			err = tx.Where("model_name = ?", managedModel.ID).First(&existing).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				existing = model.Model{
					ModelName:    managedModel.ID,
					Description:  managedModel.Description,
					Icon:         managedModel.Icon,
					Tags:         managedModel.Tags,
					VendorID:     vendorID,
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
					"icon":          managedModel.Icon,
					"tags":          managedModel.Tags,
					"vendor_id":     vendorID,
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
	for index := range channels {
		model.CacheUpdateChannel(&channels[index])
	}
	model.InvalidatePricingCache()

	previousSet := make(map[string]struct{}, len(previousActiveIDs))
	for _, modelID := range previousActiveIDs {
		previousSet[modelID] = struct{}{}
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
		err := tx.Where(&model.Option{Key: key}).First(&option).Error
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

type QiniuSyncSource struct {
	Name       string
	SourceTag  string
	ManagedTag string
	Channel    *model.Channel
	Catalog    QiniuCatalog
}

type QiniuSyncConfig struct {
	Pricing    QiniuResourcePackagePricing
	ManagedTag string
}

type QiniuModelSynchronizer struct {
	Catalog QiniuCatalog
	Now     func() time.Time
	Apply   func(context.Context, QiniuCandidateSnapshot, QiniuApplyOptions) (QiniuSyncSummary, error)
}

func (s QiniuModelSynchronizer) SyncSources(ctx context.Context, sources []QiniuSyncSource, config QiniuSyncConfig) (QiniuSyncSummary, error) {
	if len(sources) == 0 {
		return QiniuSyncSummary{}, errors.New("qiniu sync sources are empty")
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	seenCallable := make(map[string]struct{})
	combined := QiniuCandidateSnapshot{Rejected: make(map[string]QiniuAdmissionReason)}
	routes := make([]QiniuChannelRoute, 0, len(sources))
	publicCount := 0
	for _, source := range sources {
		name := strings.TrimSpace(source.Name)
		if name == "" {
			name = strings.TrimSpace(source.ManagedTag)
		}
		if source.Channel == nil {
			return QiniuSyncSummary{}, fmt.Errorf("%s managed channel is missing", name)
		}
		if source.Channel.Status != common.ChannelStatusEnabled {
			return QiniuSyncSummary{}, fmt.Errorf("%s managed channel is disabled", name)
		}
		if strings.TrimSpace(source.Channel.Key) == "" {
			return QiniuSyncSummary{}, fmt.Errorf("%s managed channel API key is empty", name)
		}
		if source.Catalog == nil {
			return QiniuSyncSummary{}, fmt.Errorf("%s catalog client is missing", name)
		}
		managedTag := strings.TrimSpace(source.ManagedTag)
		if managedTag == "" {
			return QiniuSyncSummary{}, fmt.Errorf("%s managed channel tag is empty", name)
		}
		callableIDs, err := source.Catalog.FetchCallableModelIDs(ctx, source.Channel.Key)
		if err != nil {
			return QiniuSyncSummary{}, fmt.Errorf("fetch %s callable model catalog: %w", name, err)
		}
		if len(callableIDs) == 0 {
			return QiniuSyncSummary{}, fmt.Errorf("refusing empty %s callable model catalog", name)
		}
		marketplaceModels, err := source.Catalog.FetchMarketplaceModels(ctx)
		if err != nil {
			return QiniuSyncSummary{}, fmt.Errorf("fetch %s marketplace metadata: %w", name, err)
		}
		publicCount += len(marketplaceModels)
		exclusiveIDs := make([]string, 0, len(callableIDs))
		for _, callableID := range callableIDs {
			modelID := strings.TrimSpace(callableID)
			if modelID == "" {
				continue
			}
			if _, exists := seenCallable[modelID]; !exists {
				exclusiveIDs = append(exclusiveIDs, modelID)
			}
			seenCallable[modelID] = struct{}{}
		}
		sourceTag := strings.TrimSpace(source.SourceTag)
		if sourceTag == "" {
			sourceTag = name
		}
		snapshot, err := buildQiniuCandidateSnapshot(exclusiveIDs, marketplaceModels, config.Pricing, now, sourceTag)
		if err != nil {
			return QiniuSyncSummary{}, fmt.Errorf("build %s candidate snapshot: %w", name, err)
		}
		if len(exclusiveIDs) > 0 && len(snapshot.Models) == 0 {
			return QiniuSyncSummary{}, fmt.Errorf("refusing empty %s accepted snapshot: callable=%d", name, len(exclusiveIDs))
		}
		for modelID, reason := range snapshot.Rejected {
			combined.Rejected[modelID] = reason
		}
		combined.Models = append(combined.Models, snapshot.Models...)
		routes = append(routes, QiniuChannelRoute{ManagedTag: managedTag, ModelIDs: snapshot.ActiveModelIDs()})
	}
	if len(combined.Models) == 0 {
		return QiniuSyncSummary{}, errors.New("refusing to apply empty combined qiniu snapshot")
	}
	sort.Slice(combined.Models, func(i, j int) bool { return combined.Models[i].ID < combined.Models[j].ID })
	apply := s.Apply
	if apply == nil {
		apply = ApplyQiniuCandidateSnapshot
	}
	summary, err := apply(ctx, combined, QiniuApplyOptions{ManagedTag: config.ManagedTag, Routes: routes})
	if err != nil {
		return QiniuSyncSummary{}, fmt.Errorf("apply combined qiniu snapshot: %w", err)
	}
	summary.Callable = len(seenCallable)
	summary.Public = publicCount
	summary.Accepted = len(combined.Models)
	summary.Hidden = len(combined.Rejected)
	return summary, nil
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
	snapshot, err := BuildQiniuCandidateSnapshot(callableIDs, marketplaceModels, config.Pricing, now)
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
