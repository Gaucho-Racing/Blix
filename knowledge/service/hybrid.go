package service

import (
	"cmp"
	"slices"
)

func fuseRanks[T any](lexical, semantic []T, key func(T) string, score func(*T, float64), limit int) []T {
	type ranked struct {
		hit         T
		value       float64
		lexicalRank int
		id          string
	}
	hits := map[string]*ranked{}
	for branch, list := range [][]T{lexical, semantic} {
		seen := map[string]bool{}
		for rank, hit := range list {
			id := key(hit)
			if seen[id] {
				continue
			}
			seen[id] = true
			item := hits[id]
			if item == nil {
				item = &ranked{hit: hit, id: id, lexicalRank: 100000}
				hits[id] = item
			}
			item.value += 1 / float64(60+rank+1)
			if branch == 0 {
				item.lexicalRank = rank
			}
		}
	}
	ordered := make([]*ranked, 0, len(hits))
	for _, h := range hits {
		ordered = append(ordered, h)
	}
	slices.SortFunc(ordered, func(a, b *ranked) int {
		if c := cmp.Compare(b.value, a.value); c != 0 {
			return c
		}
		if c := cmp.Compare(a.lexicalRank, b.lexicalRank); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	})
	result := make([]T, 0, min(limit, len(ordered)))
	for _, h := range ordered[:min(limit, len(ordered))] {
		score(&h.hit, h.value)
		result = append(result, h.hit)
	}
	return result
}
