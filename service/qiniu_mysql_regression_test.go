package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type qiniuSQLCaptureLogger struct {
	queries []string
}

func (capture *qiniuSQLCaptureLogger) LogMode(logger.LogLevel) logger.Interface      { return capture }
func (capture *qiniuSQLCaptureLogger) Info(context.Context, string, ...interface{})  {}
func (capture *qiniuSQLCaptureLogger) Warn(context.Context, string, ...interface{})  {}
func (capture *qiniuSQLCaptureLogger) Error(context.Context, string, ...interface{}) {}
func (capture *qiniuSQLCaptureLogger) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	query, _ := sql()
	capture.queries = append(capture.queries, query)
}

func TestLoadQiniuBillingOptionsTxQuotesKeyForMySQL(t *testing.T) {
	capture := &qiniuSQLCaptureLogger{}
	database, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "new-api:test@tcp(localhost:3306)/new-api?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: capture})
	require.NoError(t, err)

	_, _, _ = loadQiniuBillingOptionsTx(database)
	require.NotEmpty(t, capture.queries)
	for _, query := range capture.queries {
		assert.Contains(t, query, "WHERE `options`.`key` =")
		assert.NotContains(t, strings.ToLower(query), " where key = ")
	}
}
