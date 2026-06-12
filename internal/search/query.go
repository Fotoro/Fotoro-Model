package search

import (
	"strings"
)

// QueryExpander handles vague user prompts and expands them for better retrieval
type QueryExpander struct{}

func NewQueryExpander() *QueryExpander {
	return &QueryExpander{}
}

// Expand takes a vague user prompt and returns an expanded, search-optimized query
func (qe *QueryExpander) Expand(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return ""
	}

	// Detect pronouns / vague references and expand
	pronounMap := map[string][]string{
		"me":       {"person", "face", "portrait", "self"},
		"myself":   {"person", "face", "portrait", "self"},
		"us":       {"people", "group", "friends", "family"},
		"we":       {"people", "group", "friends", "family"},
		"my":       {"personal", "private"},
		"mom":      {"woman", "mother", "family"},
		"dad":      {"man", "father", "family"},
		"dog":      {"animal", "pet", "dog"},
		"cat":      {"animal", "pet", "cat"},
		"pet":      {"animal", "pet", "dog", "cat"},
		"food":     {"food", "meal", "dish", "cuisine", "restaurant"},
		"laptop":   {"laptop", "computer", "screen", "device", "technology"},
		"phone":    {"phone", "mobile", "smartphone", "screen", "device"},
		"car":      {"car", "vehicle", "automobile", "driving", "road"},
		"bike":     {"motorcycle", "bike", "bicycle", "riding"},
		"trip":     {"travel", "vacation", "trip", "tourism"},
		"vacation": {"travel", "vacation", "beach", "mountain", "hotel"},
		"party":    {"people", "party", "celebration", "night", "friends"},
		"work":     {"office", "document", "computer", "meeting", "desk"},
		"study":    {"book", "document", "paper", "desk", "laptop"},
		"screenshot": {"screenshot", "screen", "interface", "app", "text"},
		"wallpaper": {"wallpaper", "pattern", "abstract", "art", "background"},
		"receipt":  {"document", "receipt", "text", "paper", "invoice"},
		"notes":    {"document", "text", "paper", "handwriting", "notebook"},
	}

	words := strings.Fields(q)
	var expanded []string
	seen := make(map[string]bool)

	for _, w := range words {
		w = strings.TrimSuffix(w, "s") // simple stem
		if syn, ok := pronounMap[w]; ok {
			for _, s := range syn {
				if !seen[s] {
					expanded = append(expanded, s)
					seen[s] = true
				}
			}
		} else {
			if !seen[w] {
				expanded = append(expanded, w)
				seen[w] = true
			}
		}
	}

	// If query is very short (1-2 words), add category synonyms
	if len(words) <= 2 {
		categoryBoost := map[string][]string{
			"beach":    {"ocean", "sand", "sea", "coast", "waves"},
			"mountain": {"hill", "peak", "trek", "nature", "landscape"},
			"city":     {"urban", "street", "building", "architecture", "night"},
			"baby":     {"child", "infant", "kid", "family"},
			"wedding":  {"bride", "groom", "marriage", "ceremony", "people"},
			"birthday": {"cake", "party", "celebration", "people"},
		}
		for w := range seen {
			if boost, ok := categoryBoost[w]; ok {
				for _, b := range boost {
					if !seen[b] {
						expanded = append(expanded, b)
						seen[b] = true
					}
				}
			}
		}
	}

	return strings.Join(expanded, " ")
}
