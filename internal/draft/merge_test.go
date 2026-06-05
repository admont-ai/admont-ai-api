package draft

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestThreeWayMerge_DraftWins_WhenBaseEqualsCurrent(t *testing.T) {
	merged, hasConflicts, conflictText := ThreeWayMerge("base content", "base content", "draft content")
	assert.False(t, hasConflicts)
	assert.Empty(t, conflictText)
	assert.Equal(t, "draft content", merged)
}

func TestThreeWayMerge_CurrentWins_WhenBaseEqualsDraft(t *testing.T) {
	merged, hasConflicts, conflictText := ThreeWayMerge("base content", "current content", "base content")
	assert.False(t, hasConflicts)
	assert.Empty(t, conflictText)
	assert.Equal(t, "current content", merged)
}

func TestThreeWayMerge_IdenticalChanges(t *testing.T) {
	merged, hasConflicts, conflictText := ThreeWayMerge("base", "same change", "same change")
	assert.False(t, hasConflicts)
	assert.Empty(t, conflictText)
	assert.Equal(t, "same change", merged)
}

func TestThreeWayMerge_Conflict(t *testing.T) {
	merged, hasConflicts, conflictText := ThreeWayMerge("base", "current change", "draft change")
	assert.True(t, hasConflicts)
	assert.Empty(t, merged)
	assert.Contains(t, conflictText, "Conflict detected")
	assert.Contains(t, conflictText, "<<<<<<< CURRENT")
	assert.Contains(t, conflictText, "current change")
	assert.Contains(t, conflictText, "=======")
	assert.Contains(t, conflictText, "draft change")
	assert.Contains(t, conflictText, ">>>>>>> DRAFT")
}

func TestThreeWayMerge_EmptyBase(t *testing.T) {
	merged, hasConflicts, _ := ThreeWayMerge("", "", "new draft")
	assert.False(t, hasConflicts)
	assert.Equal(t, "new draft", merged)
}

func TestThreeWayMerge_AllEmpty(t *testing.T) {
	merged, hasConflicts, _ := ThreeWayMerge("", "", "")
	assert.False(t, hasConflicts)
	assert.Equal(t, "", merged)
}

func TestThreeWayMerge_ConflictNewlines(t *testing.T) {
	_, hasConflicts, conflictText := ThreeWayMerge("base\n", "current\n", "draft\n")
	assert.True(t, hasConflicts)
	assert.True(t, strings.Contains(conflictText, "current\n"))
	assert.True(t, strings.Contains(conflictText, "draft\n"))
}

func TestThreeWayMerge_ConflictNoTrailingNewline(t *testing.T) {
	_, hasConflicts, conflictText := ThreeWayMerge("base", "current", "draft")
	assert.True(t, hasConflicts)
	assert.Contains(t, conflictText, "current\n=======")
	assert.Contains(t, conflictText, "draft\n>>>>>>>")
}

func TestThreeWayMerge_MultilineContent(t *testing.T) {
	base := "line1\nline2\nline3"
	current := "line1\nchanged-line2\nline3"
	draft := "line1\nline2\nnew-line3"

	_, hasConflicts, conflictText := ThreeWayMerge(base, current, draft)
	assert.True(t, hasConflicts)
	assert.Contains(t, conflictText, "changed-line2")
	assert.Contains(t, conflictText, "new-line3")
}
