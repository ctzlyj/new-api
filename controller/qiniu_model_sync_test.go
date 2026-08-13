package controller

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestQiniuModelSyncHandlerDefaults(t *testing.T) {
	t.Setenv("QINIU_MODEL_SYNC_ENABLED", "true")
	t.Setenv("QINIU_MODEL_SYNC_INTERVAL_HOURS", "")
	t.Setenv("QINIU_MODEL_PRICE_MARKUP", "")
	t.Setenv("QINIU_MANAGED_CHANNEL_TAG", "")

	handler := qiniuModelSyncHandler{}

	assert.True(t, handler.Enabled())
	assert.Equal(t, 24*time.Hour, handler.Interval())
	assert.Equal(t, model.SystemTaskTypeQiniuModelSync, handler.Type())
	assert.Equal(t, 0.05, qiniuModelPriceMarkup())
	assert.Equal(t, "qiniu-managed", qiniuManagedChannelTag())
}

func TestQiniuModelSyncHandlerUsesEnvironment(t *testing.T) {
	t.Setenv("QINIU_MODEL_SYNC_ENABLED", "false")
	t.Setenv("QINIU_MODEL_SYNC_INTERVAL_HOURS", "12")
	t.Setenv("QINIU_MODEL_PRICE_MARKUP", "0.08")
	t.Setenv("QINIU_MANAGED_CHANNEL_TAG", "custom-qiniu")

	handler := qiniuModelSyncHandler{}

	assert.False(t, handler.Enabled())
	assert.Equal(t, 12*time.Hour, handler.Interval())
	assert.Equal(t, 0.08, qiniuModelPriceMarkup())
	assert.Equal(t, "custom-qiniu", qiniuManagedChannelTag())
}

func TestQiniuModelSyncHandlerRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("QINIU_MODEL_SYNC_INTERVAL_HOURS", "0")
	t.Setenv("QINIU_MODEL_PRICE_MARKUP", "-1")

	assert.Equal(t, 24*time.Hour, qiniuModelSyncHandler{}.Interval())
	assert.Equal(t, 0.05, qiniuModelPriceMarkup())
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
