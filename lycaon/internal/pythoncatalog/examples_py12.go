package pythoncatalog

// taskSummaryForPY12Distribution returns concise task-oriented discovery phrases
// for the 7 PY12 saved-evidence troubleshooting distributions.
func taskSummaryForPY12Distribution(normalizedName string) string {
	switch normalizedName {
	case "jc":
		return "parse saved diagnostic command output (ss, netstat, dig, clf) with explicit parser names"
	case "dpkt":
		return "reconstruct offline network timelines from saved pcap and pcapng captures"
	case "dnspython":
		return "decode saved DNS wire messages and zone text offline (allow_include=False)"
	case "asn1crypto":
		return "inspect PEM/DER X.509 certificate metadata, SANs and SPKI fingerprints offline"
	case "drain3":
		return "compare before/after unstructured log templates in memory (persistence_handler=None)"
	case "parse":
		return "extract typed fields from repeated text log layouts and count unmatched lines"
	case "pyelftools":
		return "inspect saved ELF headers, architecture, DT_NEEDED and build ID cross-platform"
	default:
		return ""
	}
}

// py12OnDemandExamples returns curated on-demand usage snippets for PY12
// saved-evidence troubleshooting packages and existing stdlib workflows.
func py12OnDemandExamples() []CapabilityExample {
	return []CapabilityExample{
		{
			ID:           "jc-diagnostics",
			Title:        "Parse saved ss/netstat/dig/clf output with jc",
			Distribution: "jc",
			TaskPhrases:  []string{"diagnostic output", "parse ss", "parse netstat", "parse dig", "parse clf", "jc"},
			Snippet:      "import jc\nrows = jc.parse('ss', text)       # Linux ss output\nans = jc.parse('dig', dig_text)   # DNS dig output\nlogs = jc.parse('clf', clf_text)  # Common Log Format",
		},
		{
			ID:           "dpkt-capture",
			Title:        "Read saved pcap/pcapng timeline with dpkt",
			Distribution: "dpkt",
			TaskPhrases:  []string{"saved captures", "pcap", "pcapng", "network timeline", "dpkt"},
			Snippet:      "import dpkt\nwith open('capture.pcap', 'rb') as f:\n    r = dpkt.pcap.Reader(f)\n    assert r.datalink() == dpkt.pcap.DLT_EN10MB\n    for ts, buf in r:\n        eth = dpkt.ethernet.Ethernet(buf)",
		},
		{
			ID:           "dnspython-offline",
			Title:        "Decode saved DNS wire bytes and zone text offline",
			Distribution: "dnspython",
			TaskPhrases:  []string{"dns wire", "dns zone", "dnspython", "nxdomain"},
			Snippet:      "import dns.message, dns.zone\nmsg = dns.message.from_wire(raw_bytes)\nz = dns.zone.from_text(zone_text, origin='example.test.', allow_include=False)",
		},
		{
			ID:           "asn1crypto-cert",
			Title:        "Inspect PEM/DER certificate metadata with asn1crypto",
			Distribution: "asn1crypto",
			TaskPhrases:  []string{"certificate metadata", "pem", "der", "x509", "asn1crypto"},
			Snippet:      "import hashlib\nfrom asn1crypto import pem, x509\nif pem.detect(raw): _, _, raw = pem.unarmor(raw)\ncert = x509.Certificate.load(raw)\nspki_sha256 = hashlib.sha256(cert['tbs_certificate']['subject_public_key_info'].dump()).hexdigest()",
		},
		{
			ID:           "drain3-log-compare",
			Title:        "Compare before/after unstructured logs in memory with Drain3",
			Distribution: "drain3",
			TaskPhrases:  []string{"log comparison", "unstructured logs", "rare failure", "drain3"},
			Snippet:      "from drain3 import TemplateMiner\nminer = TemplateMiner(persistence_handler=None)\nfor lineno, line in enumerate(lines, 1):\n    res = miner.add_log_message(line)",
		},
		{
			ID:           "parse-typed-logs",
			Title:        "Extract typed fields and count unmatched lines with parse",
			Distribution: "parse",
			TaskPhrases:  []string{"extract typed fields", "custom log lines", "parse"},
			Snippet:      "import parse\npat = parse.compile('[{ts}] status={status:d} elapsed_ms={elapsed:f}')\nmatched, unmatched = [], []\nfor lineno, line in enumerate(lines, 1):\n    m = pat.parse(line)\n    (matched if m else unmatched).append((lineno, m))",
		},
		{
			ID:           "pyelftools-inspect",
			Title:        "Inspect saved ELF architecture, DT_NEEDED and build ID",
			Distribution: "pyelftools",
			TaskPhrases:  []string{"elf", "dt_needed", "build id", "pyelftools", "elftools"},
			Snippet:      "from elftools.elf.elffile import ELFFile\nwith open('artifact.elf', 'rb') as f:\n    ef = ELFFile(f)\n    machine = ef['e_machine']",
		},
		{
			ID:           "har-timing-json",
			Title:        "Summarize HAR redirects and non-overlapping timings with json",
			StdlibModule: "json",
			TaskPhrases:  []string{"har timing", "har redirects", "har"},
			Snippet:      "import json\nhar = json.loads(har_text)\nfor e in har['log']['entries']:\n    t = e['timings']\n    # Exclude unavailable (-1) values and do not double-count ssl inside connect; omit headers/cookies\n    total_ms = sum(t[k] for k in ('blocked','dns','connect','send','wait','receive') if t.get(k, -1) >= 0)",
		},
		{
			ID:           "rotated-compressed-logs",
			Title:        "Read rotated gzip and bz2 logs with bounded expansion",
			StdlibModule: "bz2",
			TaskPhrases:  []string{"rotated logs", "gzip", "bz2", "compressed logs"},
			Snippet:      "import bz2, gzip\nMAX_BYTES = 65536\nwith gzip.open('app.log.1.gz', 'rb') as f:\n    data = f.read(MAX_BYTES + 1)\n    truncated = len(data) > MAX_BYTES",
		},
	}
}
