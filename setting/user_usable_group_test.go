package setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultUserUsableGroupsExcludeRetiredGroups(t *testing.T) {
	assert.NotContains(t, userUsableGroups, "AA-vip")
	assert.NotContains(t, userUsableGroups, "vip")
}
