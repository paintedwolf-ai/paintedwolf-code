package webresearch

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Fixture-specific scoring compares seed plans across prompt runs.

// seedQualityReport scores how SERP-like a seed plan is for a query.
type seedQualityReport struct {
	LeadCount       int
	JournalismLeads int // non-weak publisher hosts (docs, news, reference, etc.)
	WeakHostLeads   int
	DriftExpand     int
	HostBreadth     int
}

var weakSeedHosts = []string{
	"store.steampowered.com",
	"en.wikipedia.org",
	"wikipedia.org",
	"reddit.com",
	"old.reddit.com",
}

func scoreSeedPlan(query string, plan seedPlan) seedQualityReport {
	report := seedQualityReport{LeadCount: len(plan.leads)}
	hosts := map[string]struct{}{}
	for _, lead := range plan.leads {
		host := normalizeLeadHost(lead.host)
		if host != "" {
			hosts[host] = struct{}{}
		}
		if isWeakSeedHost(host) {
			report.WeakHostLeads++
		} else if isJournalismHost(host) {
			report.JournalismLeads++
		}
	}
	report.HostBreadth = len(hosts)
	for _, phrase := range plan.expand {
		if seedDriftPhrase(query, phrase) {
			report.DriftExpand++
		}
	}
	return report
}

func seedPlanMeetsBar(query string, plan seedPlan) (bool, string) {
	if len(plan.leads) < 4 {
		return false, "fewer than 4 leads"
	}
	report := scoreSeedPlan(query, plan)
	if report.JournalismLeads < 3 {
		return false, "fewer than 3 authoritative publisher leads"
	}
	if report.WeakHostLeads > 1 {
		return false, "too many weak-host leads (store/wiki/reddit)"
	}
	if report.DriftExpand > 1 {
		return false, "expand drifts off-topic"
	}
	if report.HostBreadth < 3 {
		return false, "lead host breadth < 3"
	}
	return true, ""
}

func isWeakSeedHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, weak := range weakSeedHosts {
		if host == weak || strings.HasSuffix(host, "."+weak) {
			return true
		}
	}
	return false
}

// isJournalismHost treats vendor and institutional hosts as non-journalism so
// the quality bar counts independent publisher coverage only.
func isJournalismHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || isWeakSeedHost(host) {
		return false
	}
	switch {
	case strings.Contains(host, "steamdeck.com"),
		strings.Contains(host, "valvesoftware.com"),
		strings.HasSuffix(host, ".gov"),
		strings.HasSuffix(host, ".edu"):
		return false
	default:
		return true
	}
}

// seedDriftPhrase flags expand lines that jump to a different product category
// than the query anchor terms suggest.
func seedDriftPhrase(query, phrase string) bool {
	q := strings.ToLower(query)
	p := strings.ToLower(phrase)
	if strings.Contains(q, "steam machine") || strings.Contains(q, "steammachine") {
		drift := []string{
			"mini pc", "small form factor", "sff pc", "htpc", "nuc ",
			"steam deck", "handheld", "rog ally", "legion go", "msi claw",
		}
		for _, d := range drift {
			if strings.Contains(p, d) && !strings.Contains(p, "steam machine") {
				return true
			}
		}
	}
	return false
}

func TestSeedPlanMeetsBar_goodFixture(t *testing.T) {
	plan, err := parseSeedsJSON(`{"seeds":["https://www.theverge.com","https://www.pcgamer.com","https://arstechnica.com"],"fresh":true,"expand":["Steam Machine review","Valve Steam Machine 2026"],"leads":[{"title":"Steam Machine review","host":"www.theverge.com"},{"title":"Steam Machine hardware review","host":"arstechnica.com"},{"title":"Steam Machine benchmarks","host":"www.pcgamer.com"},{"title":"Steam Machine verdict","host":"www.ign.com"}]}`)
	testutil.FailErr(t, "parseSeedsJSON failed", err)
	ok, why := seedPlanMeetsBar("steam machine reviews", plan)
	if !ok {
		t.Fatalf("good fixture failed: %s", why)
	}
}

func TestSeedPlanMeetsBar_debugBadMiniPC(t *testing.T) {
	// Coordinator query drifted to mini PCs.
	plan, err := parseSeedsJSON(`{"seeds":["https://www.pcgamer.com/","https://www.tomshardware.com/best-picks/best-mini-pc","https://www.reddit.com/r/SteamOS/","https://store.steampowered.com/steamos/"],"fresh":true,"expand":["best mini PC for SteamOS 2025","quiet SFF PC for living room gaming","best small form factor SteamOS console"],"leads":[{"title":"Best Mini PC for Gaming in 2025","host":"www.pcgamer.com"},{"title":"The Best Mini PCs for 2025","host":"www.tomshardware.com"},{"title":"SteamOS on Reddit","host":"www.reddit.com"},{"title":"SteamOS","host":"store.steampowered.com"}]}`)
	testutil.FailErr(t, "parseSeedsJSON failed", err)
	ok, _ := seedPlanMeetsBar("steam machine reviews", plan)
	if ok {
		t.Fatal("mini PC drift fixture should not pass steam machine bar")
	}
	report := scoreSeedPlan("steam machine reviews", plan)
	if report.DriftExpand < 2 {
		t.Fatalf("drift expand = %d want >= 2", report.DriftExpand)
	}
}

func TestSeedPlanMeetsBar_debugBadOfficialStore(t *testing.T) {
	// Official store and wiki hosts dominate the leads.
	plan, err := parseSeedsJSON(`{"seeds":["https://store.steampowered.com/steamos","https://en.wikipedia.org/wiki/Steam_Machine","https://www.pcgamer.com"],"fresh":true,"expand":["Steam Machine 2025","Valve console 2026"],"leads":[{"title":"SteamOS — Valve","host":"www.valvesoftware.com"},{"title":"Steam Machine - Wikipedia","host":"en.wikipedia.org"},{"title":"Steam store","host":"store.steampowered.com"},{"title":"r/SteamOS","host":"www.reddit.com"}]}`)
	testutil.FailErr(t, "parseSeedsJSON failed", err)
	ok, _ := seedPlanMeetsBar("Steam Machine 2026 hardware console review", plan)
	if ok {
		t.Fatal("store/wiki dominated fixture should not pass review bar")
	}
}
