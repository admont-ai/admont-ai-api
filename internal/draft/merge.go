package draft

import (
	"fmt"
	"strings"
)

// ThreeWayMerge performs a three-way merge between base, current, and draft content.
// Returns the merged result, whether conflicts exist, and conflict text with markers.
func ThreeWayMerge(base, current, draft string) (merged string, hasConflicts bool, conflictText string) {
	// Fast path: if base == current, no one else edited -> draft wins
	if base == current {
		return draft, false, ""
	}

	// Fast path: if base == draft, user didn't change anything -> current wins
	if base == draft {
		return current, false, ""
	}

	// Fast path: if current == draft, both made identical changes
	if current == draft {
		return current, false, ""
	}

	// Conflict: both sides changed from base differently
	var sb strings.Builder
	sb.WriteString("<<<<<<< CURRENT (committed version)\n")
	sb.WriteString(current)
	if !strings.HasSuffix(current, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("=======\n")
	sb.WriteString(draft)
	if !strings.HasSuffix(draft, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString(">>>>>>> DRAFT (your version)\n")

	conflictResult := sb.String()
	return "", true, fmt.Sprintf("Conflict detected: the file was modified after your draft was created.\n\n%s", conflictResult)
}
