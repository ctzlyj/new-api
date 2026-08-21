package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupQiniuApplyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := model.DB
	database, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.Model{}, &model.Vendor{}, &model.Option{}))
	model.DB = database
	common.OptionMapRWMutex.Lock()
	previousOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()

	oldModes, err := common.Marshal(billing_setting.GetBillingModeCopy())
	require.NoError(t, err)
	oldExpressions, err := common.Marshal(billing_setting.GetBillingExprCopy())
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, model.ApplyOptionUpdates(map[string]string{
			"billing_setting.billing_mode": string(oldModes),
			"billing_setting.billing_expr": string(oldExpressions),
		}))
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousOptionMap
		common.OptionMapRWMutex.Unlock()
	})
	return database
}

func seedQiniuManagedState(t *testing.T, database *gorm.DB) model.Channel {
	t.Helper()
	tag := "qiniu-managed"
	channel := model.Channel{
		Type:   constant.ChannelTypeOpenAI,
		Key:    "test-secret",
		Status: common.ChannelStatusEnabled,
		Name:   "Qiniu Managed",
		Group:  "default",
		Models: "old-model",
		Tag:    &tag,
	}
	require.NoError(t, database.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(database))
	require.NoError(t, database.Create(&model.Model{ModelName: "old-model", Status: 1, ManagedBy: tag}).Error)
	require.NoError(t, database.Create(&model.Model{ModelName: "SF-gpt-image-2", Status: 1}).Error)

	modes := map[string]string{"old-model": billing_setting.BillingModeTieredExpr, "SF-gpt-image-2": billing_setting.BillingModeRatio}
	expressions := map[string]string{"old-model": `v1:tier("old", p * 1)`, "SF-gpt-image-2": `v1:tier("image", p * 1)`}
	modeJSON, err := common.Marshal(modes)
	require.NoError(t, err)
	expressionJSON, err := common.Marshal(expressions)
	require.NoError(t, err)
	values := map[string]string{
		"billing_setting.billing_mode": string(modeJSON),
		"billing_setting.billing_expr": string(expressionJSON),
	}
	require.NoError(t, model.UpdateOptionsTx(database, values))
	require.NoError(t, model.ApplyOptionUpdates(values))
	return channel
}

func qiniuApplySnapshot(modelID string) QiniuCandidateSnapshot {
	return QiniuCandidateSnapshot{Models: []QiniuManagedModel{{
		ID:          modelID,
		Name:        "New Model",
		Description: "Managed by Qiniu",
		BillingExpr: `v1:tier("qiniu_0", p * 1.05 + c * 2.1)`,
		Endpoints:   []constant.EndpointType{constant.EndpointTypeOpenAI},
	}}, Rejected: map[string]QiniuAdmissionReason{}}
}

func TestApplyQiniuCandidateSnapshotReplacesManagedState(t *testing.T) {
	database := setupQiniuApplyTestDB(t)
	channel := seedQiniuManagedState(t, database)

	summary, err := ApplyQiniuCandidateSnapshot(context.Background(), qiniuApplySnapshot("new-model"), QiniuApplyOptions{ManagedTag: "qiniu-managed"})

	require.NoError(t, err)
	assert.Equal(t, 1, summary.Added)
	assert.Equal(t, 1, summary.Removed)
	var updatedChannel model.Channel
	require.NoError(t, database.First(&updatedChannel, channel.Id).Error)
	assert.Equal(t, "new-model", updatedChannel.Models)
	var abilities []model.Ability
	require.NoError(t, database.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "new-model", abilities[0].Model)
	var oldModel model.Model
	require.NoError(t, database.Where("model_name = ?", "old-model").First(&oldModel).Error)
	assert.Equal(t, 0, oldModel.Status)
	var newModel model.Model
	require.NoError(t, database.Where("model_name = ?", "new-model").First(&newModel).Error)
	assert.Equal(t, 1, newModel.Status)
	assert.Equal(t, "qiniu-managed", newModel.ManagedBy)
	var imageModel model.Model
	require.NoError(t, database.Where("model_name = ?", "SF-gpt-image-2").First(&imageModel).Error)
	assert.Equal(t, 1, imageModel.Status)
	assert.Equal(t, billing_setting.BillingModeRatio, billing_setting.GetBillingMode("SF-gpt-image-2"))
	_, oldExists := billing_setting.GetBillingExpr("old-model")
	assert.False(t, oldExists)
	_, newExists := billing_setting.GetBillingExpr("new-model")
	assert.True(t, newExists)
}

func TestApplyQiniuCandidateSnapshotPersistsCatalogMetadata(t *testing.T) {
	database := setupQiniuApplyTestDB(t)
	seedQiniuManagedState(t, database)
	snapshot := qiniuApplySnapshot("qwen3-235b-a22b")
	snapshot.Models[0].Icon = "https://static.qiniu.com/ai-inference/model-icons/qwen.png"
	snapshot.Models[0].Tags = "Qiniu,工具调用,深度思考,文本输入,文本输出"
	snapshot.Models[0].VendorName = "Aliyun"
	snapshot.Models[0].VendorIcon = snapshot.Models[0].Icon

	_, err := ApplyQiniuCandidateSnapshot(context.Background(), snapshot, QiniuApplyOptions{ManagedTag: "qiniu-managed"})

	require.NoError(t, err)
	var vendor model.Vendor
	require.NoError(t, database.Where("name = ?", "Aliyun").First(&vendor).Error)
	assert.Equal(t, snapshot.Models[0].VendorIcon, vendor.Icon)
	var catalogModel model.Model
	require.NoError(t, database.Where("model_name = ?", "qwen3-235b-a22b").First(&catalogModel).Error)
	assert.Equal(t, snapshot.Models[0].Icon, catalogModel.Icon)
	assert.Equal(t, snapshot.Models[0].Tags, catalogModel.Tags)
	assert.Equal(t, vendor.Id, catalogModel.VendorID)
}

func TestApplyQiniuCandidateSnapshotRollsBackOnOptionFailure(t *testing.T) {
	database := setupQiniuApplyTestDB(t)
	channel := seedQiniuManagedState(t, database)
	require.NoError(t, database.Exec(`CREATE TRIGGER fail_qiniu_option_update BEFORE UPDATE ON options BEGIN SELECT RAISE(ABORT, 'forced option failure'); END`).Error)

	_, err := ApplyQiniuCandidateSnapshot(context.Background(), qiniuApplySnapshot("new-model"), QiniuApplyOptions{ManagedTag: "qiniu-managed"})

	require.Error(t, err)
	var updatedChannel model.Channel
	require.NoError(t, database.First(&updatedChannel, channel.Id).Error)
	assert.Equal(t, "old-model", updatedChannel.Models)
	var oldModel model.Model
	require.NoError(t, database.Where("model_name = ?", "old-model").First(&oldModel).Error)
	assert.Equal(t, 1, oldModel.Status)
	var newCount int64
	require.NoError(t, database.Model(&model.Model{}).Where("model_name = ?", "new-model").Count(&newCount).Error)
	assert.Zero(t, newCount)
	_, oldExists := billing_setting.GetBillingExpr("old-model")
	assert.True(t, oldExists)
	_, newExists := billing_setting.GetBillingExpr("new-model")
	assert.False(t, newExists)
}
func TestApplyQiniuCandidateSnapshotRejectsEmptySnapshot(t *testing.T) {
	database := setupQiniuApplyTestDB(t)
	channel := seedQiniuManagedState(t, database)

	_, err := ApplyQiniuCandidateSnapshot(context.Background(), QiniuCandidateSnapshot{}, QiniuApplyOptions{ManagedTag: "qiniu-managed"})

	require.ErrorContains(t, err, "empty")
	var updatedChannel model.Channel
	require.NoError(t, database.First(&updatedChannel, channel.Id).Error)
	assert.Equal(t, "old-model", updatedChannel.Models)
}

func TestApplyQiniuCandidateSnapshotRejectsUnmanagedModelCollision(t *testing.T) {
	database := setupQiniuApplyTestDB(t)
	channel := seedQiniuManagedState(t, database)
	require.NoError(t, database.Create(&model.Model{
		ModelName:   "manual-model",
		Description: "Managed manually",
		Status:      1,
	}).Error)

	_, err := ApplyQiniuCandidateSnapshot(context.Background(), qiniuApplySnapshot("manual-model"), QiniuApplyOptions{ManagedTag: "qiniu-managed"})

	require.ErrorContains(t, err, "not owned")
	var updatedChannel model.Channel
	require.NoError(t, database.First(&updatedChannel, channel.Id).Error)
	assert.Equal(t, "old-model", updatedChannel.Models)
	var manualModel model.Model
	require.NoError(t, database.Where("model_name = ?", "manual-model").First(&manualModel).Error)
	assert.Equal(t, "Managed manually", manualModel.Description)
	assert.Empty(t, manualModel.ManagedBy)
}
