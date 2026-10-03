package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/kernel/metrics"
)

// GetMetrics returns the latest Brutal sample. It does not change rates.
func GetMetrics(ctx *gin.Context) {
	common.ResponseSuccess(ctx, metrics.Current())
}
