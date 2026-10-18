package installed

import (
	"fmt"

	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

type fixture struct {
	file, source, rule string
	line               int
}

var fixtures = []fixture{
	{"main.rs", "use reqwest::Client;\nfn main() { let client = Client::builder().danger_accept_invalid_certs(true); }", "lycaon.rust.tls-verification-disabled", 2},
	{"app.vue", "<template><div>Hi</div></template>\n<script setup lang=\"ts\">const code: string = location.hash; eval(code);</script>", "lycaon.javascript.code-injection", 2},
	{"run.ps1", "<#\niex ((New-Object Net.WebClient).DownloadString($example))\n#>\niex ((New-Object Net.WebClient).DownloadString($url))\n", "lycaon.powershell.download-execution", 4},
	{"safe.html", "<!-- <script>eval(location.hash)</script> -->", "", 0},
	{"ignored.vue", "<script>eval(location.hash)</script>", "", 0},
	{"data.proto", "syntax = \"proto3\"; message Example { string name = 1; }", "", 0},
	{".semgrepignore", "ignored.vue\n", "", 0},
}

// Case is one deterministic product behavior, including negative controls.
type Case struct {
	File         string `json:"file"`
	ExpectedRule string `json:"expected_rule,omitempty"`
	Findings     int    `json:"findings"`
	Passed       bool   `json:"passed"`
}

func verifyResult(project string, result *scanoutput.Result) ([]Case, error) {
	if result == nil || result.FindingsCount != len(result.Findings) || len(result.Warnings) != 0 {
		return nil, fmt.Errorf("installed scan returned missing, inconsistent, or partial results")
	}
	scanoutput.NormalizeResultPaths(result, project)
	expected := make(map[string]fixture, len(fixtures))
	counts := make(map[string]int)
	for _, fixture := range fixtures {
		expected[fixture.file] = fixture
	}
	for _, finding := range result.Findings {
		if len(finding.Locations) != 1 {
			return nil, fmt.Errorf("installed scan finding requires one primary source location")
		}
		location := finding.Locations[0]
		fixture, known := expected[location.URI]
		if !known || fixture.rule == "" || finding.RuleID != fixture.rule || location.StartLine != fixture.line {
			return nil, fmt.Errorf("installed scan finding differs from expected original source")
		}
		valid := true
		scanfindings.VisitFindingLocations(&finding, func(evidence *api.SecurityFindingLocation) {
			valid = valid && evidence.URI == fixture.file && evidence.StartLine == fixture.line
		})
		if !valid || (fixture.file == "app.vue" && (finding.Dataflow == nil || finding.Dataflow.Source == nil || finding.Dataflow.Sink == nil)) {
			return nil, fmt.Errorf("installed scan evidence was not mapped to original source")
		}
		counts[fixture.file]++
	}
	var cases []Case
	complete := true
	for _, fixture := range fixtures {
		want := 0
		if fixture.rule != "" {
			want = 1
		}
		passed := counts[fixture.file] == want
		complete = complete && passed
		cases = append(cases, Case{File: fixture.file, ExpectedRule: fixture.rule, Findings: counts[fixture.file], Passed: passed})
	}
	if !complete {
		return cases, fmt.Errorf("installed scan omitted or duplicated a required finding")
	}
	return cases, nil
}
