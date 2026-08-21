package controller

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func withQiniuCustomPointRate(t *testing.T, rate float64) {
	t.Helper()
	setting := operation_setting.GetGeneralSetting()
	previous := *setting
	setting.CustomCurrencyExchangeRate = rate
	t.Cleanup(func() { *setting = previous })
}

func TestQiniuModelSyncHandlerDefaults(t *testing.T) {
	t.Setenv("QINIU_MODEL_SYNC_ENABLED", "true")
	t.Setenv("QINIU_MODEL_SYNC_INTERVAL_HOURS", "")
	t.Setenv("QINIU_RESOURCE_PACKAGE_COST_CNY_PER_100M", "")
	t.Setenv("QINIU_RESOURCE_PACKAGE_SALE_CNY_PER_100M", "")
	t.Setenv("QINIU_POINTS_PER_CNY", "")
	t.Setenv("QINIU_MANAGED_CHANNEL_TAG", "")
	withQiniuCustomPointRate(t, 68)

	handler := qiniuModelSyncHandler{}
	pricing := qiniuResourcePackagePricing()

	assert.True(t, handler.Enabled())
	assert.Equal(t, 24*time.Hour, handler.Interval())
	assert.Equal(t, model.SystemTaskTypeQiniuModelSync, handler.Type())
	assert.Equal(t, 323.0, pricing.CostCNYPer100MTokens)
	assert.Equal(t, 350.0, pricing.SaleCNYPer100MTokens)
	assert.Equal(t, 20.0, pricing.PointsPerCNY)
	assert.Equal(t, 68.0, pricing.DisplayPointsPerQuotaUnit)
	assert.Equal(t, "qiniu-managed", qiniuManagedChannelTag())
	assert.Equal(t, "modelink-managed", modelinkManagedChannelTag())
}

func TestQiniuModelSyncHandlerUsesEnvironment(t *testing.T) {
	t.Setenv("QINIU_MODEL_SYNC_ENABLED", "false")
	t.Setenv("QINIU_MODEL_SYNC_INTERVAL_HOURS", "12")
	t.Setenv("QINIU_RESOURCE_PACKAGE_COST_CNY_PER_100M", "324")
	t.Setenv("QINIU_RESOURCE_PACKAGE_SALE_CNY_PER_100M", "360")
	t.Setenv("QINIU_POINTS_PER_CNY", "21")
	t.Setenv("QINIU_MANAGED_CHANNEL_TAG", "custom-qiniu")
	t.Setenv("MODELINK_MANAGED_CHANNEL_TAG", "custom-modelink")
	withQiniuCustomPointRate(t, 72)

	handler := qiniuModelSyncHandler{}
	pricing := qiniuResourcePackagePricing()

	assert.False(t, handler.Enabled())
	assert.Equal(t, 12*time.Hour, handler.Interval())
	assert.Equal(t, 324.0, pricing.CostCNYPer100MTokens)
	assert.Equal(t, 360.0, pricing.SaleCNYPer100MTokens)
	assert.Equal(t, 21.0, pricing.PointsPerCNY)
	assert.Equal(t, 72.0, pricing.DisplayPointsPerQuotaUnit)
	assert.Equal(t, "custom-qiniu", qiniuManagedChannelTag())
	assert.Equal(t, "custom-modelink", modelinkManagedChannelTag())
}

func TestQiniuModelSyncHandlerRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("QINIU_MODEL_SYNC_INTERVAL_HOURS", "0")
	t.Setenv("QINIU_RESOURCE_PACKAGE_COST_CNY_PER_100M", "-1")
	t.Setenv("QINIU_RESOURCE_PACKAGE_SALE_CNY_PER_100M", "bad")
	t.Setenv("QINIU_POINTS_PER_CNY", "0")
	t.Setenv("MODELINK_MANAGED_CHANNEL_TAG", "   ")
	withQiniuCustomPointRate(t, 68)

	pricing := qiniuResourcePackagePricing()

	assert.Equal(t, 24*time.Hour, qiniuModelSyncHandler{}.Interval())
	assert.Equal(t, 323.0, pricing.CostCNYPer100MTokens)
	assert.Equal(t, 350.0, pricing.SaleCNYPer100MTokens)
	assert.Equal(t, 20.0, pricing.PointsPerCNY)
	assert.Equal(t, "modelink-managed", modelinkManagedChannelTag())
}

func setupQiniuSystemTaskControllerDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	database, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
}

func TestStartQiniuModelSyncReusesActiveTask(t *testing.T) {
	setupQiniuSystemTaskControllerDB(t)
	gin.SetMode(gin.TestMode)

	firstRecorder := httptest.NewRecorder()
	firstContext, _ := gin.CreateTestContext(firstRecorder)
	StartQiniuModelSync(firstContext)

	assert.Contains(t, firstRecorder.Body.String(), `"success":true`)
	assert.Contains(t, firstRecorder.Body.String(), `"created":true`)

	secondRecorder := httptest.NewRecorder()
	secondContext, _ := gin.CreateTestContext(secondRecorder)
	StartQiniuModelSync(secondContext)

	assert.Contains(t, secondRecorder.Body.String(), `"success":true`)
	assert.Contains(t, secondRecorder.Body.String(), `"created":false`)
	var count int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("type = ?", model.SystemTaskTypeQiniuModelSync).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
