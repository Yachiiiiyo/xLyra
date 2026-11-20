package router

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func spreadCandidate(name string, priority float64, health string) Candidate {
	candidate := Candidate{}
	candidate.Site.Name = name
	candidate.Site.RoutingPriority = priority
	candidate.Model.SiteModelID = uuid.New()
	candidate.Health.Status = health
	return candidate
}

func leadCounts(items []Candidate, seeds int) map[string]int {
	counts := map[string]int{}
	for i := 0; i < seeds; i++ {
		ordered, _ := spreadCandidates(items, fmt.Sprintf("conversation-%d", i), nil)
		counts[ordered[0].Site.Name]++
	}
	return counts
}

func TestSpreadKeepsPriorityAsAHardPreference(t *testing.T) {
	t.Parallel()

	items := []Candidate{
		spreadCandidate("top-a", 4.7, "healthy"),
		spreadCandidate("top-b", 4.7, "healthy"),
		spreadCandidate("lower", 4.5, "healthy"),
	}
	for i := 0; i < 200; i++ {
		ordered, pick := spreadCandidates(items, "", nil)
		if ordered[2].Site.Name != "lower" {
			t.Fatalf("order = %s,%s,%s: the lower priority must stay behind its tied betters", ordered[0].Site.Name, ordered[1].Site.Name, ordered[2].Site.Name)
		}
		if pick.Mode != RoutePickRandom || pick.TierSize != 2 {
			t.Fatalf("pick = %#v, want a random pick over a tier of 2", pick)
		}
		for index, candidate := range ordered {
			if candidate.Rank != index+1 {
				t.Fatalf("rank of %s = %d, want %d", candidate.Site.Name, candidate.Rank, index+1)
			}
		}
	}
}

func TestSpreadLeavesALoneLeaderAndCoolingAlone(t *testing.T) {
	t.Parallel()

	cooling := spreadCandidate("cooling", 4.7, "healthy")
	cooling.Cooling = true
	items := []Candidate{spreadCandidate("lead", 4.7, "healthy"), cooling, spreadCandidate("other", 4.5, "healthy")}
	ordered, pick := spreadCandidates(items, "seed", nil)
	for index, want := range []string{"lead", "cooling", "other"} {
		if ordered[index].Site.Name != want {
			t.Fatalf("position %d = %s, want %s: a cooling candidate is not on the same footing as a healthy one", index, ordered[index].Site.Name, want)
		}
	}
	if pick.Mode != RoutePickPriority || pick.TierSize != 1 {
		t.Fatalf("pick = %#v, want a priority pick", pick)
	}
	if items[0].Rank != 0 {
		t.Fatal("the input slice must not be modified")
	}
}

func TestSpreadWithSeedIsStableAndSplitsConversationsEvenly(t *testing.T) {
	t.Parallel()

	items := []Candidate{spreadCandidate("a", 4.7, "healthy"), spreadCandidate("b", 4.7, "healthy")}
	first, pick := spreadCandidates(items, "conversation-7", nil)
	if pick.Mode != RoutePickSticky {
		t.Fatalf("pick = %#v, want sticky with a seed", pick)
	}
	for i := 0; i < 50; i++ {
		again, _ := spreadCandidates(items, "conversation-7", nil)
		if again[0].Site.Name != first[0].Site.Name || again[1].Site.Name != first[1].Site.Name {
			t.Fatal("one conversation must always get the same order")
		}
	}
	counts := leadCounts(items, 4000)
	for name, count := range counts {
		if count < 1800 || count > 2200 {
			t.Fatalf("%s leads %d of 4000 conversations, want about half", name, count)
		}
	}
}

func TestSpreadWeightsFavourHealthyCandidates(t *testing.T) {
	t.Parallel()

	items := []Candidate{spreadCandidate("healthy", 4.7, "healthy"), spreadCandidate("degraded", 4.7, "degraded")}
	counts := leadCounts(items, 6000)
	// weights 1 : 0.5, so the healthy candidate should lead about two thirds.
	if share := float64(counts["healthy"]) / 6000; share < 0.62 || share > 0.71 {
		t.Fatalf("healthy share = %.3f, want about 0.667 (counts %v)", share, counts)
	}

	flaky := spreadCandidate("flaky", 4.7, "healthy")
	rate := 0.2
	flaky.Health.ModelSuccessRate = &rate
	flaky.Health.ModelRequestCount = 200
	fresh := spreadCandidate("fresh", 4.7, "healthy")
	counts = leadCounts([]Candidate{flaky, fresh}, 6000)
	if counts["flaky"] >= counts["fresh"]/3 {
		t.Fatalf("flaky leads %d against %d: a model failing 80%% of requests should lead far less", counts["flaky"], counts["fresh"])
	}

	sparse := spreadCandidate("sparse", 4.7, "healthy")
	sparse.Health.ModelSuccessRate = &rate
	sparse.Health.ModelRequestCount = spreadMinModelSamples - 1
	if weight := spreadWeight(sparse); weight != 1 {
		t.Fatalf("weight = %v, a success rate over too few requests must not count", weight)
	}
}

func TestSpreadOnlyMovesConversationsOfTheCandidateThatLeft(t *testing.T) {
	t.Parallel()

	a, b, c := spreadCandidate("a", 4.7, "healthy"), spreadCandidate("b", 4.7, "healthy"), spreadCandidate("c", 4.7, "healthy")
	moved, stayed := 0, 0
	for i := 0; i < 3000; i++ {
		seed := fmt.Sprintf("conversation-%d", i)
		before, _ := spreadCandidates([]Candidate{a, b, c}, seed, nil)
		after, _ := spreadCandidates([]Candidate{a, c}, seed, nil) // b is demoted away
		if before[0].Site.Name == "b" {
			moved++
			continue
		}
		stayed++
		if after[0].Site.Name != before[0].Site.Name {
			t.Fatalf("conversation %d led by %s moved to %s although only b left", i, before[0].Site.Name, after[0].Site.Name)
		}
	}
	if moved == 0 || stayed == 0 {
		t.Fatalf("moved=%d stayed=%d, expected both kinds of conversation", moved, stayed)
	}
}

func TestSpreadRandomModeUsesTheInjectedSource(t *testing.T) {
	t.Parallel()

	items := []Candidate{spreadCandidate("a", 4.7, "healthy"), spreadCandidate("b", 4.7, "healthy")}
	// a draws the high value, b the low one: -ln(u)/w is smallest for the high u.
	values := []float64{0.9, 0.1}
	index := 0
	ordered, _ := spreadCandidates(items, "", func() float64 {
		value := values[index%len(values)]
		index++
		return value
	})
	if ordered[0].Site.Name != "a" {
		t.Fatalf("lead = %s, want a for the larger draw", ordered[0].Site.Name)
	}
	empty, pick := spreadCandidates(nil, "x", nil)
	if len(empty) != 0 || pick.Mode != RoutePickPriority {
		t.Fatalf("empty input = %#v %#v", empty, pick)
	}
}
