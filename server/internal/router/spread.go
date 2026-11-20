package router

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"sort"
)

// RoutePick describes how the first candidate of a plan was chosen, so request
// logs can show whether spreading and stickiness are doing their job.
type RoutePick struct {
	// Mode is "priority" when one candidate led alone, "sticky" when the lead of
	// an equal-priority group was picked by a stable seed, "random" otherwise.
	Mode string
	// TierSize is the number of equal-priority candidates competing for the lead.
	TierSize int
}

const (
	RoutePickPriority = "priority"
	RoutePickSticky   = "sticky"
	RoutePickRandom   = "random"

	// spreadMinModelSamples is how many recent requests a model needs before its
	// success rate may lower its share.
	spreadMinModelSamples = 20
)

// spreadCandidates reorders candidates that sit on the same footing: equal
// cooling state and equal routing priority. Priority stays a hard preference;
// only ties are shuffled. A seed (a conversation) always maps to the same order
// while the candidate set is unchanged, so a conversation keeps hitting one
// site and its prompt cache; with no seed the order is random. Either way a
// candidate leads with probability proportional to its weight, and when one of
// them drops out only the conversations that led there move.
//
// It is weighted rendezvous hashing: each candidate gets -ln(u)/weight for a
// uniform u derived from the seed and the candidate, and the smallest wins.
func spreadCandidates(items []Candidate, seed string, random func() float64) ([]Candidate, RoutePick) {
	pick := RoutePick{Mode: RoutePickPriority, TierSize: 1}
	if len(items) == 0 {
		return items, pick
	}
	if random == nil {
		random = rand.Float64
	}
	ordered := append([]Candidate(nil), items...)
	for start := 0; start < len(ordered); {
		end := start + 1
		for end < len(ordered) && sameFooting(ordered[start], ordered[end]) {
			end++
		}
		if end-start > 1 {
			spreadTier(ordered[start:end], seed, random)
			if start == 0 {
				pick.TierSize = end - start
				pick.Mode = RoutePickRandom
				if seed != "" {
					pick.Mode = RoutePickSticky
				}
			}
		}
		start = end
	}
	for index := range ordered {
		ordered[index].Rank = index + 1
	}
	return ordered, pick
}

func sameFooting(a, b Candidate) bool {
	return a.Cooling == b.Cooling && a.Site.RoutingPriority == b.Site.RoutingPriority
}

func spreadTier(tier []Candidate, seed string, random func() float64) {
	keys := make([]float64, len(tier))
	for index, candidate := range tier {
		u := spreadUniform(seed, candidate, random)
		keys[index] = -math.Log(u) / spreadWeight(candidate)
	}
	sort.Sort(&tierByKey{tier: tier, keys: keys})
}

type tierByKey struct {
	tier []Candidate
	keys []float64
}

func (t *tierByKey) Len() int           { return len(t.tier) }
func (t *tierByKey) Less(i, j int) bool { return t.keys[i] < t.keys[j] }
func (t *tierByKey) Swap(i, j int) {
	t.tier[i], t.tier[j] = t.tier[j], t.tier[i]
	t.keys[i], t.keys[j] = t.keys[j], t.keys[i]
}

// spreadUniform returns a number in (0,1): derived from the seed and the
// candidate when there is a seed, random otherwise.
func spreadUniform(seed string, candidate Candidate, random func() float64) float64 {
	if seed == "" {
		u := random()
		if u <= 0 || u >= 1 {
			return 0.5
		}
		return u
	}
	sum := sha256.Sum256([]byte(seed + "\x00" + candidate.Model.SiteModelID.String()))
	bits := binary.BigEndian.Uint64(sum[:8]) >> 11 // 53 bits
	return (float64(bits) + 0.5) / (1 << 53)
}

// spreadWeight is the share a tied candidate gets. Equal priority means equal
// footing, so operators stay in control through priority; the weight only
// lowers the share of a candidate that is visibly unwell.
func spreadWeight(candidate Candidate) float64 {
	weight := 1.0
	switch candidate.Health.Status {
	case "healthy":
	case "degraded":
		weight = 0.5
	case "unknown", "":
		weight = 0.7
	default:
		weight = 0.1
	}
	if rate := candidate.Health.ModelSuccessRate; rate != nil && candidate.Health.ModelRequestCount >= spreadMinModelSamples {
		weight *= math.Min(1, math.Max(0.1, *rate))
	}
	return weight
}
