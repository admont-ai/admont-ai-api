package llm

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// maxModelAge is the cutoff after which a model (with a known release date) is
// considered legacy and hidden from selection. Providers keep deprecated
// models listed in their APIs for a long time; age is the only generic signal.
const maxModelAge = 15 * 30 * 24 * time.Hour // ~15 months

// nonChatPattern matches model IDs that are not general chat models
// (embeddings, speech, image, moderation, search adjuncts, ...).
var nonChatPattern = regexp.MustCompile(
	`(?i)embed|whisper|tts|audio|dall-e|image|moderation|realtime|transcribe|search|rerank|guard|batch`)

// snapshotSuffix matches dated/versioned snapshot suffixes such as
// "-2024-08-06", "-20250929", "-0125", "-preview-05-20", "-001".
var snapshotSuffix = regexp.MustCompile(
	`-(20\d{2}-\d{2}-\d{2}|20\d{6}|\d{4}|\d{3}|preview(-\d{2}-\d{2})?|latest)$`)

// FilterModels reduces a provider's raw model list to current, chat-capable
// models: it drops non-chat models, collapses dated snapshots into one entry
// per family, drops models older than maxModelAge, and sorts newest-first.
// Ollama lists are returned unfiltered — local models are user-managed.
func FilterModels(providerType string, models []Model) []Model {
	if providerType == "ollama" {
		return models
	}

	// Capability filter.
	var chat []Model
	for _, m := range models {
		if nonChatPattern.MatchString(m.ID) {
			continue
		}
		chat = append(chat, m)
	}

	// Snapshot collapse: group by family (ID with snapshot suffixes stripped),
	// preferring the undated alias, else the newest snapshot.
	type candidate struct {
		model   Model
		isAlias bool
	}
	families := make(map[string]candidate)
	var order []string
	for _, m := range chat {
		family := m.ID
		for {
			stripped := snapshotSuffix.ReplaceAllString(family, "")
			if stripped == family {
				break
			}
			family = stripped
		}
		isAlias := family == m.ID
		cur, seen := families[family]
		if !seen {
			families[family] = candidate{m, isAlias}
			order = append(order, family)
			continue
		}
		// Undated alias wins; among snapshots the newest (by Created, then ID) wins.
		if cur.isAlias {
			continue
		}
		if isAlias || m.Created > cur.model.Created ||
			(m.Created == cur.model.Created && m.ID > cur.model.ID) {
			families[family] = candidate{m, isAlias}
		}
	}

	collapsed := make([]Model, 0, len(order))
	for _, f := range order {
		collapsed = append(collapsed, families[f].model)
	}

	// Age cutoff: drop dated models past the cutoff, but never return an
	// empty list if the provider had chat models — keep the newest one.
	cutoff := time.Now().Add(-maxModelAge).Unix()
	var current []Model
	for _, m := range collapsed {
		if m.Created > 0 && m.Created < cutoff {
			continue
		}
		current = append(current, m)
	}
	if len(current) == 0 && len(collapsed) > 0 {
		newest := collapsed[0]
		for _, m := range collapsed[1:] {
			if m.Created > newest.Created {
				newest = m
			}
		}
		current = []Model{newest}
	}

	sort.SliceStable(current, func(i, j int) bool {
		if current[i].Created != current[j].Created {
			return current[i].Created > current[j].Created
		}
		return strings.Compare(current[i].ID, current[j].ID) < 0
	})
	return current
}
