package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultGroupRatiosExcludeRetiredGroups(t *testing.T) {
	assert.NotContains(t, defaultGroupRatio, "AA-vip")
	assert.NotContains(t, defaultGroupRatio, "vip")
	assert.NotContains(t, defaultGroupGroupRatio, "AA-vip")
	assert.NotContains(t, defaultGroupGroupRatio, "vip")
}
