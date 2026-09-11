package registry

import "encoding/json"

// Keep explicit compatibility routes when a remote catalog stops advertising them.
// Remote definitions still win when they include the same ID.
func isCompatibilityModel(id string) bool {
	switch id {
	case "gpt-5.4", "gpt-5.4-mini", "gemini-3-flash-agent", "gemini-3.5-flash-low", "gemini-3.5-flash-extra-low":
		return true
	}
	return false
}

func retainCompatibilityModels(incoming, current []*ModelInfo) []*ModelInfo {
	seen := make(map[string]bool, len(incoming))
	for _, m := range incoming {
		if m != nil {
			seen[m.ID] = true
		}
	}
	for _, m := range current {
		if m != nil && isCompatibilityModel(m.ID) && !seen[m.ID] {
			incoming = append(incoming, cloneModelInfos([]*ModelInfo{m})[0])
			seen[m.ID] = true
		}
	}
	return incoming
}

func retainCompatibilityCatalog(incoming, current *staticModelsJSON) {
	if incoming == nil || current == nil {
		return
	}
	incoming.CodexFree = retainCompatibilityModels(incoming.CodexFree, current.CodexFree)
	incoming.CodexTeam = retainCompatibilityModels(incoming.CodexTeam, current.CodexTeam)
	incoming.CodexPlus = retainCompatibilityModels(incoming.CodexPlus, current.CodexPlus)
	incoming.CodexPro = retainCompatibilityModels(incoming.CodexPro, current.CodexPro)
	incoming.Antigravity = retainCompatibilityModels(incoming.Antigravity, current.Antigravity)
}

func retainCompatibilityClientModels(data, current []byte) []byte {
	var incoming, previous codexClientModelsPayload
	if json.Unmarshal(data, &incoming) != nil || json.Unmarshal(current, &previous) != nil {
		return data
	}
	seen := make(map[string]bool, len(incoming.Models))
	for _, m := range incoming.Models {
		if id, ok := m["slug"].(string); ok {
			seen[id] = true
		}
	}
	changed := false
	for _, m := range previous.Models {
		id, _ := m["slug"].(string)
		if isCompatibilityModel(id) && !seen[id] {
			incoming.Models = append(incoming.Models, m)
			seen[id] = true
			changed = true
		}
	}
	if !changed {
		return data
	}
	encoded, err := json.Marshal(incoming)
	if err != nil {
		return data
	}
	return encoded
}
