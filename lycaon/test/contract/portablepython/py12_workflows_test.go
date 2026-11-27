package portablepython

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func py12FixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "py12", name))
	contractcheck.FailErr(t, "read py12 fixture "+name, err)
	return b
}

func copyPY12Fixture(t *testing.T, wsDir, name string) {
	t.Helper()
	dst := filepath.Join(wsDir, name)
	contractcheck.FailErr(t, "mkdir py12 fixture parent", os.MkdirAll(filepath.Dir(dst), 0o755))
	contractcheck.FailErr(t, "write py12 fixture "+name, os.WriteFile(dst, py12FixtureBytes(t, name), 0o644))
}

func newPY12Service(t *testing.T, receiptDir string) *portablepython.Service {
	t.Helper()
	requireStrict := os.Getenv("PW_REQUIRE_PY12") == "1" || os.Getenv("PW_REQUIRE_NATIVE_QUAL") == "1"
	if requireStrict {
		if strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) != "" {
			t.Fatalf("%s must be UNSET during PY12 qualification", portablepython.EnvImageDir)
		}
		if strings.TrimSpace(os.Getenv(pythonruntime.EnvEngineDir)) != "" {
			t.Fatalf("%s must be UNSET during PY12 qualification", pythonruntime.EnvEngineDir)
		}
	}
	if root := strings.TrimSpace(os.Getenv("PW_QUAL_ENGINE_ROOT")); root != "" && strings.TrimSpace(os.Getenv("LYCAON_ENGINE_ROOT")) == "" {
		t.Setenv("LYCAON_ENGINE_ROOT", root)
	}
	cfg := portablepython.ServiceConfig{
		ScratchRoot: t.TempDir(),
	}
	if receiptDir != "" {
		sink, err := portablepython.NewFileReceiptStore(receiptDir)
		contractcheck.FailErr(t, "create FileReceiptStore", err)
		cfg.ReceiptSink = sink
	}
	svc, err := portablepython.NewService(context.Background(), cfg)
	if err != nil {
		if requireStrict {
			t.Fatalf("failed to initialize PY12 Portable Python service: %v", err)
		}
		t.Skipf("no staged Portable Python service available (set LYCAON_ENGINE_ROOT): %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	info := svc.RuntimeInfo()
	if info.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("PY12 default RuntimeInfo.Backend=%q want %q on %s/%s", info.Backend, pythonruntime.BackendNativeAOT, runtime.GOOS, runtime.GOARCH)
	}
	return svc
}

func runPY12Code(t *testing.T, svc *portablepython.Service, wsDir, execID, code string, args []string) *portablepython.ToolResponse {
	t.Helper()
	roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: wsDir}}
	rawArgs := map[string]any{"code": code}
	if len(args) > 0 {
		rawArgs["args"] = args
	}
	var forbiddenOps []string
	resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
		ExecutionID:  execID,
		CallID:       execID + "-call",
		SessionID:    "py12-troubleshooting",
		Actor:        "coordinator",
		View:         "primary",
		Surface:      "implement_investigate",
		RawArgs:      rawArgs,
		Roots:        roots,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		Policy: func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
			if op.Family == pythonruntime.EffectSubprocess || op.Family == pythonruntime.EffectHTTP {
				forbiddenOps = append(forbiddenOps, fmt.Sprintf("%s:%s", op.Family, op.Operation))
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionDeny, Reason: "py12_offline_parsing_only"}
			}
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
	})
	contractcheck.FailErr(t, "execute "+execID, err)
	if len(forbiddenOps) > 0 {
		t.Fatalf("%s attempted forbidden external operations during offline parsing: %v", execID, forbiddenOps)
	}
	if !resp.CleanCompletion || resp.ExitCode != 0 {
		t.Fatalf("%s failed: exit=%d clean=%v stop=%+v diag=%v stdout=%q stderr=%q",
			execID, resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.RuntimeDiagnostics, resp.Stdout, resp.Stderr)
	}
	return resp
}

func readJSONResult(t *testing.T, path string, dst any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read json "+path, err)
	contractcheck.FailErr(t, "unmarshal json "+path, json.Unmarshal(raw, dst))
}

func py12SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestPY12_SavedEvidenceWorkflows qualifies all 7 PY12 troubleshooting
// libraries, 3 existing stdlib workflows (HAR/json, SQLite, rotated logs),
// and authority controls using independent Go/host oracles.
func TestPY12_SavedEvidenceWorkflows(t *testing.T) {
	svc := newPY12Service(t, t.TempDir())

	t.Run("01_jc_saved_diagnostics_and_quoted_command_transfer", func(t *testing.T) {
		ws := t.TempDir()
		for _, f := range []string{"ss_linux.txt", "netstat_macos.txt", "dig_output.txt", "access_clf.log", "malformed_empty.txt"} {
			copyPY12Fixture(t, ws, f)
		}
		// Pass frozen authorized Command output as quoted argv data (zero second command launch).
		quotedDig := string(py12FixtureBytes(t, "dig_output.txt"))

		runPY12Code(t, svc, ws, "py12-jc", `
import json, sys
from pathlib import Path
import jc

ss_rows = jc.parse("ss", Path("ss_linux.txt").read_text(encoding="utf-8"))
ns_rows = jc.parse("netstat", Path("netstat_macos.txt").read_text(encoding="utf-8"))
dig_rows = jc.parse("dig", Path("dig_output.txt").read_text(encoding="utf-8"))
clf_rows = jc.parse("clf", Path("access_clf.log").read_text(encoding="utf-8"))
empty_rows = jc.parse("clf", Path("malformed_empty.txt").read_text(encoding="utf-8"))
quoted_dig = jc.parse("dig", sys.argv[1])

ss_ports = sorted(int(r["local_port"]) for r in ss_rows if r.get("local_port") is not None)
ns_states = sorted(str(r.get("state", "")) for r in ns_rows if r.get("state"))
dig_answers = sorted(a["data"] for q in dig_rows for a in q.get("answer", []))
quoted_answers = sorted(a["data"] for q in quoted_dig for a in q.get("answer", []))
clf_statuses = [int(r["status"]) for r in clf_rows]

out = {
    "ss_ports": ss_ports,
    "ns_states": ns_states,
    "dig_status": dig_rows[0]["status"],
    "dig_answers": dig_answers,
    "quoted_answers": quoted_answers,
    "clf_statuses": clf_statuses,
    "empty_count": len(empty_rows),
}
Path("jc_result.json").write_text(json.dumps(out), encoding="utf-8")
`, []string{quotedDig})

		var res struct {
			SSPorts       []int    `json:"ss_ports"`
			NSStates      []string `json:"ns_states"`
			DigStatus     string   `json:"dig_status"`
			DigAnswers    []string `json:"dig_answers"`
			QuotedAnswers []string `json:"quoted_answers"`
			CLFStatuses   []int    `json:"clf_statuses"`
			EmptyCount    int      `json:"empty_count"`
		}
		readJSONResult(t, filepath.Join(ws, "jc_result.json"), &res)
		if !slices.Equal(res.SSPorts, []int{443, 5353, 5432, 8080}) {
			t.Fatalf("jc ss ports = %v, want [443 5353 5432 8080]", res.SSPorts)
		}
		if !slices.Equal(res.NSStates, []string{"CLOSE_WAIT", "ESTABLISHED", "LISTEN"}) {
			t.Fatalf("jc netstat states = %v, want [CLOSE_WAIT ESTABLISHED LISTEN]", res.NSStates)
		}
		if res.DigStatus != "NOERROR" || !slices.Equal(res.DigAnswers, []string{"203.0.113.10", "203.0.113.11"}) {
			t.Fatalf("jc dig mismatch: status=%q answers=%v", res.DigStatus, res.DigAnswers)
		}
		if !slices.Equal(res.QuotedAnswers, res.DigAnswers) {
			t.Fatalf("jc quoted Command arg transfer mismatch: %v vs %v", res.QuotedAnswers, res.DigAnswers)
		}
		if !slices.Equal(res.CLFStatuses, []int{200, 302, 404, 502}) || res.EmptyCount != 0 {
			t.Fatalf("jc clf/empty mismatch: statuses=%v empty=%d", res.CLFStatuses, res.EmptyCount)
		}
	})

	t.Run("02_dpkt_pcap_and_pcapng_network_timeline", func(t *testing.T) {
		ws := t.TempDir()
		for _, f := range []string{"timeline.pcap", "timeline.pcapng", "truncated.pcap", "unsupported_linktype.pcap"} {
			copyPY12Fixture(t, ws, f)
		}

		runPY12Code(t, svc, ws, "py12-dpkt", `
import ipaddress, json
from pathlib import Path
import dpkt

def summarize_reader(reader):
    dns_queries = {}
    dns_rtt_ms = {}
    syn_ts = None
    synack_rtt_ms = None
    rst_seen = False
    pkt_count = 0
    for ts, buf in reader:
        pkt_count += 1
        eth = dpkt.ethernet.Ethernet(buf)
        ip = eth.data
        src = str(ipaddress.IPv4Address(ip.src))
        dst = str(ipaddress.IPv4Address(ip.dst))
        if isinstance(ip.data, dpkt.udp.UDP):
            udp = ip.data
            dns_pkt = dpkt.dns.DNS(udp.data)
            if dns_pkt.qr == dpkt.dns.DNS_Q:
                dns_queries[dns_pkt.id] = (ts, dns_pkt.qd[0].name, f"{src}->{dst}")
            elif dns_pkt.qr == dpkt.dns.DNS_R and dns_pkt.id in dns_queries:
                qts, qname, _ = dns_queries.pop(dns_pkt.id)
                dns_rtt_ms[qname] = round((ts - qts) * 1000.0, 2)
        elif isinstance(ip.data, dpkt.tcp.TCP):
            tcp = ip.data
            if (tcp.flags & dpkt.tcp.TH_SYN) and not (tcp.flags & dpkt.tcp.TH_ACK):
                syn_ts = ts
            elif (tcp.flags & dpkt.tcp.TH_SYN) and (tcp.flags & dpkt.tcp.TH_ACK) and syn_ts is not None:
                synack_rtt_ms = round((ts - syn_ts) * 1000.0, 2)
            elif tcp.flags & dpkt.tcp.TH_RST:
                rst_seen = True
    return {
        "pkt_count": pkt_count,
        "dns_rtt_ms": dns_rtt_ms,
        "unanswered_dns": sorted(v[1] for v in dns_queries.values()),
        "synack_rtt_ms": synack_rtt_ms,
        "rst_seen": rst_seen,
    }

with open("timeline.pcap", "rb") as f:
    pcap_sum = summarize_reader(dpkt.pcap.Reader(f))
with open("timeline.pcapng", "rb") as f:
    pcapng_sum = summarize_reader(dpkt.pcapng.Reader(f))

# Truncated capture: parse each packet; truncated trailing frame raises NeedData
trunc_pkts = 0
trunc_detected = False
try:
    with open("truncated.pcap", "rb") as f:
        r = dpkt.pcap.Reader(f)
        for _ts, _buf in r:
            dpkt.ethernet.Ethernet(_buf)
            trunc_pkts += 1
except (dpkt.NeedData, dpkt.UnpackError, ValueError, EOFError):
    trunc_detected = True

# Unsupported linktype (147 = DLT_USER0 != DLT_EN10MB)
with open("unsupported_linktype.pcap", "rb") as f:
    ur = dpkt.pcap.Reader(f)
    unsupported_dlt = ur.datalink()

out = {
    "pcap": pcap_sum,
    "pcapng": pcapng_sum,
    "trunc_pkts": trunc_pkts,
    "trunc_detected": trunc_detected,
    "unsupported_dlt": unsupported_dlt,
}
Path("dpkt_result.json").write_text(json.dumps(out), encoding="utf-8")
`, nil)

		var res struct {
			Pcap struct {
				PktCount      int                `json:"pkt_count"`
				DNSRTTMs      map[string]float64 `json:"dns_rtt_ms"`
				UnansweredDNS []string           `json:"unanswered_dns"`
				SynAckRTTMs   float64            `json:"synack_rtt_ms"`
				RSTSeen       bool               `json:"rst_seen"`
			} `json:"pcap"`
			PcapNG struct {
				PktCount      int                `json:"pkt_count"`
				DNSRTTMs      map[string]float64 `json:"dns_rtt_ms"`
				UnansweredDNS []string           `json:"unanswered_dns"`
				SynAckRTTMs   float64            `json:"synack_rtt_ms"`
				RSTSeen       bool               `json:"rst_seen"`
			} `json:"pcapng"`
			TruncPkts      int  `json:"trunc_pkts"`
			TruncDetected  bool `json:"trunc_detected"`
			UnsupportedDLT int  `json:"unsupported_dlt"`
		}
		readJSONResult(t, filepath.Join(ws, "dpkt_result.json"), &res)
		for name, s := range map[string]struct {
			cnt int
			dns map[string]float64
			un  []string
			syn float64
			rst bool
		}{
			"pcap":   {res.Pcap.PktCount, res.Pcap.DNSRTTMs, res.Pcap.UnansweredDNS, res.Pcap.SynAckRTTMs, res.Pcap.RSTSeen},
			"pcapng": {res.PcapNG.PktCount, res.PcapNG.DNSRTTMs, res.PcapNG.UnansweredDNS, res.PcapNG.SynAckRTTMs, res.PcapNG.RSTSeen},
		} {
			if s.cnt != 7 || math.Abs(s.dns["api.example.test"]-12.5) > 0.1 || math.Abs(s.syn-250.0) > 0.1 || !s.rst {
				t.Fatalf("dpkt %s mismatch: %+v", name, s)
			}
			if !slices.Equal(s.un, []string{"timeout.example.test"}) {
				t.Fatalf("dpkt %s unanswered DNS = %v, want [timeout.example.test]", name, s.un)
			}
		}
		if res.TruncPkts != 1 || !res.TruncDetected || res.UnsupportedDLT != 147 {
			t.Fatalf("dpkt negative controls mismatch: trunc_pkts=%d detected=%v dlt=%d", res.TruncPkts, res.TruncDetected, res.UnsupportedDLT)
		}
	})

	t.Run("03_dnspython_offline_wire_and_zone", func(t *testing.T) {
		ws := t.TempDir()
		for _, f := range []string{"dns_answer.bin", "dns_nxdomain.bin", "example.zone", "zone_with_include.zone"} {
			copyPY12Fixture(t, ws, f)
		}

		runPY12Code(t, svc, ws, "py12-dnspython", `
import json
from pathlib import Path
import dns.message, dns.rcode, dns.rdatatype, dns.zone

msg_ok = dns.message.from_wire(Path("dns_answer.bin").read_bytes())
msg_nx = dns.message.from_wire(Path("dns_nxdomain.bin").read_bytes())
z = dns.zone.from_text(Path("example.zone").read_text(encoding="utf-8"), origin="example.test.", allow_include=False)

include_rejected = False
try:
    dns.zone.from_text(Path("zone_with_include.zone").read_text(encoding="utf-8"), origin="example.test.", allow_include=False)
except Exception:
    include_rejected = True

a_rrset = msg_ok.find_rrset(msg_ok.answer, dns.name.from_text("api.example.test."), dns.rdataclass.IN, dns.rdatatype.A)
txt_rrset = msg_ok.find_rrset(msg_ok.answer, dns.name.from_text("api.example.test."), dns.rdataclass.IN, dns.rdatatype.TXT)
zone_a = z.find_rdataset("api", dns.rdatatype.A)
zone_cname = z.find_rdataset("www", dns.rdatatype.CNAME)

out = {
    "ok_rcode": dns.rcode.to_text(msg_ok.rcode()),
    "ok_a_ips": sorted(r.address for r in a_rrset),
    "ok_a_ttl": a_rrset.ttl,
    "ok_txt": [b"".join(r.strings).decode("ascii") for r in txt_rrset],
    "nx_rcode": dns.rcode.to_text(msg_nx.rcode()),
    "nx_answers": len(msg_nx.answer),
    "nx_authority_soa": len(msg_nx.authority),
    "zone_a_ips": sorted(r.address for r in zone_a),
    "zone_cname": str(list(zone_cname)[0].target.derelativize(z.origin)),
    "include_rejected": include_rejected,
}
Path("dns_result.json").write_text(json.dumps(out), encoding="utf-8")
`, nil)

		var res struct {
			OkRcode         string   `json:"ok_rcode"`
			OkAIPs          []string `json:"ok_a_ips"`
			OkATTL          int      `json:"ok_a_ttl"`
			OkTXT           []string `json:"ok_txt"`
			NxRcode         string   `json:"nx_rcode"`
			NxAnswers       int      `json:"nx_answers"`
			NxAuthoritySOA  int      `json:"nx_authority_soa"`
			ZoneAIPs        []string `json:"zone_a_ips"`
			ZoneCNAME       string   `json:"zone_cname"`
			IncludeRejected bool     `json:"include_rejected"`
		}
		readJSONResult(t, filepath.Join(ws, "dns_result.json"), &res)
		if res.OkRcode != "NOERROR" || !slices.Equal(res.OkAIPs, []string{"203.0.113.10", "203.0.113.11"}) || res.OkATTL != 300 || !slices.Equal(res.OkTXT, []string{"v=py12"}) {
			t.Fatalf("dnspython NOERROR wire mismatch: %+v", res)
		}
		if res.NxRcode != "NXDOMAIN" || res.NxAnswers != 0 || res.NxAuthoritySOA != 1 {
			t.Fatalf("dnspython NXDOMAIN wire mismatch: %+v", res)
		}
		if !slices.Equal(res.ZoneAIPs, []string{"203.0.113.10", "203.0.113.11"}) || res.ZoneCNAME != "api.example.test." || !res.IncludeRejected {
			t.Fatalf("dnspython zone/include mismatch: %+v", res)
		}
	})

	t.Run("04_asn1crypto_pem_der_vs_go_x509_oracle", func(t *testing.T) {
		ws := t.TempDir()
		for _, f := range []string{"issuer.pem", "issuer.der", "leaf.pem", "leaf.der", "spoofed_leaf.pem"} {
			copyPY12Fixture(t, ws, f)
		}

		// Independent Go crypto/x509 host oracle
		leafDER := py12FixtureBytes(t, "leaf.der")
		issuerDER := py12FixtureBytes(t, "issuer.der")
		goLeaf, err := x509.ParseCertificate(leafDER)
		contractcheck.FailErr(t, "go x509 parse leaf.der", err)
		goIssuer, err := x509.ParseCertificate(issuerDER)
		contractcheck.FailErr(t, "go x509 parse issuer.der", err)
		spoofBlock, _ := pem.Decode(py12FixtureBytes(t, "spoofed_leaf.pem"))
		if spoofBlock == nil {
			t.Fatal("failed to decode spoofed_leaf.pem")
		}
		goSpoof, err := x509.ParseCertificate(spoofBlock.Bytes)
		contractcheck.FailErr(t, "go x509 parse spoofed_leaf.pem", err)

		wantLeafSPKI := py12SHA256Hex(goLeaf.RawSubjectPublicKeyInfo)
		wantIssuerSPKI := py12SHA256Hex(goIssuer.RawSubjectPublicKeyInfo)
		wantSpoofSPKI := py12SHA256Hex(goSpoof.RawSubjectPublicKeyInfo)
		if wantSpoofSPKI == wantIssuerSPKI {
			t.Fatal("spoofed fixture must have a distinct key from issuer")
		}

		runPY12Code(t, svc, ws, "py12-asn1crypto", `
import hashlib, json
from pathlib import Path
from asn1crypto import pem as asn1_pem, x509

def load_cert(path_str):
    raw = Path(path_str).read_bytes()
    if asn1_pem.detect(raw):
        _, _, raw = asn1_pem.unarmor(raw)
    return x509.Certificate.load(raw)

leaf_pem = load_cert("leaf.pem")
leaf_der = load_cert("leaf.der")
issuer_pem = load_cert("issuer.pem")
spoof = load_cert("spoofed_leaf.pem")

assert leaf_pem.dump() == leaf_der.dump()

sans_dns = sorted(str(v) for v in leaf_pem.subject_alt_name_value.native if isinstance(v, str) and not v[0].isdigit())
sans_ip = sorted(str(v) for v in leaf_pem.subject_alt_name_value.native if isinstance(v, str) and v[0].isdigit())

def spki_sha256(cert):
    return hashlib.sha256(cert["tbs_certificate"]["subject_public_key_info"].dump()).hexdigest()

out = {
    "leaf_subject_cn": leaf_pem.subject.native["common_name"],
    "leaf_issuer_cn": leaf_pem.issuer.native["common_name"],
    "issuer_subject_cn": issuer_pem.subject.native["common_name"],
    "not_before_year": leaf_pem["tbs_certificate"]["validity"]["not_before"].native.year,
    "not_after_year": leaf_pem["tbs_certificate"]["validity"]["not_after"].native.year,
    "sans_dns": sans_dns,
    "sans_ip": sans_ip,
    "leaf_spki_sha256": spki_sha256(leaf_pem),
    "issuer_spki_sha256": spki_sha256(issuer_pem),
    "spoof_issuer_cn": spoof.issuer.native["common_name"],
    "spoof_spki_sha256": spki_sha256(spoof),
    "name_match_establishes_trust": False,
}
Path("cert_result.json").write_text(json.dumps(out), encoding="utf-8")
`, nil)

		var res struct {
			LeafSubjectCN             string   `json:"leaf_subject_cn"`
			LeafIssuerCN              string   `json:"leaf_issuer_cn"`
			IssuerSubjectCN           string   `json:"issuer_subject_cn"`
			NotBeforeYear             int      `json:"not_before_year"`
			NotAfterYear              int      `json:"not_after_year"`
			SANsDNS                   []string `json:"sans_dns"`
			SANsIP                    []string `json:"sans_ip"`
			LeafSPKI                  string   `json:"leaf_spki_sha256"`
			IssuerSPKI                string   `json:"issuer_spki_sha256"`
			SpoofIssuerCN             string   `json:"spoof_issuer_cn"`
			SpoofSPKI                 string   `json:"spoof_spki_sha256"`
			NameMatchEstablishesTrust bool     `json:"name_match_establishes_trust"`
		}
		readJSONResult(t, filepath.Join(ws, "cert_result.json"), &res)
		if res.LeafSubjectCN != goLeaf.Subject.CommonName || res.LeafIssuerCN != goLeaf.Issuer.CommonName || res.IssuerSubjectCN != goIssuer.Subject.CommonName {
			t.Fatalf("asn1crypto subject/issuer mismatch vs Go x509 oracle: %+v", res)
		}
		if res.NotBeforeYear != goLeaf.NotBefore.Year() || res.NotAfterYear != goLeaf.NotAfter.Year() {
			t.Fatalf("asn1crypto validity mismatch vs Go x509: got %d..%d want %d..%d", res.NotBeforeYear, res.NotAfterYear, goLeaf.NotBefore.Year(), goLeaf.NotAfter.Year())
		}
		wantDNS := slices.Clone(goLeaf.DNSNames)
		slices.Sort(wantDNS)
		if !slices.Equal(res.SANsDNS, wantDNS) || !slices.Equal(res.SANsIP, []string{"203.0.113.10"}) {
			t.Fatalf("asn1crypto SANs mismatch: dns=%v ip=%v", res.SANsDNS, res.SANsIP)
		}
		if res.LeafSPKI != wantLeafSPKI || res.IssuerSPKI != wantIssuerSPKI || res.SpoofSPKI != wantSpoofSPKI {
			t.Fatalf("asn1crypto SPKI SHA-256 mismatch vs Go x509 oracle")
		}
		if res.SpoofIssuerCN != res.IssuerSubjectCN || res.NameMatchEstablishesTrust {
			t.Fatalf("spoofed issuer check failed: %+v", res)
		}
	})

	t.Run("05_drain3_log_comparison_vs_counter_baseline", func(t *testing.T) {
		ws := t.TempDir()
		copyPY12Fixture(t, ws, "logs_before.txt")
		copyPY12Fixture(t, ws, "logs_after.txt")

		runPY12Code(t, svc, ws, "py12-drain3", `
import json, re
from collections import Counter
from pathlib import Path
from drain3 import TemplateMiner
from drain3.template_miner_config import TemplateMinerConfig

cfg = TemplateMinerConfig()
cfg.drain_sim_th = 0.4
cfg.drain_depth = 4
# Strictly in-memory: persistence_handler=None, no snapshots or deserialization
miner = TemplateMiner(persistence_handler=None, config=cfg)

before_lines = Path("logs_before.txt").read_text(encoding="utf-8").splitlines()
after_lines = Path("logs_after.txt").read_text(encoding="utf-8").splitlines()

before_clusters = {}
for idx, line in enumerate(before_lines, 1):
    res = miner.add_log_message(line)
    cid = res["cluster_id"]
    before_clusters.setdefault(cid, {"template": res["template_mined"], "first_line": idx, "count": 0})["count"] += 1

after_clusters = {}
for idx, line in enumerate(after_lines, 1):
    res = miner.add_log_message(line)
    cid = res["cluster_id"]
    after_clusters.setdefault(cid, {"template": res["template_mined"], "first_line": idx, "count": 0})["count"] += 1

new_clusters = [v for cid, v in after_clusters.items() if cid not in before_clusters]

# Baseline comparison: naive whitespace/digit normalization + Counter
norm = lambda s: re.sub(r"\b\d+\b", "<NUM>", s)
baseline_before = Counter(norm(l) for l in before_lines)
baseline_after = Counter(norm(l) for l in after_lines)

out = {
    "drain_before_cluster_count": len(before_clusters),
    "drain_after_cluster_count": len(after_clusters),
    "drain_new_clusters": new_clusters,
    "baseline_before_distinct": len(baseline_before),
    "baseline_after_distinct": len(baseline_after),
}
Path("drain3_result.json").write_text(json.dumps(out), encoding="utf-8")
`, nil)

		var res struct {
			DrainBeforeCount int `json:"drain_before_cluster_count"`
			DrainAfterCount  int `json:"drain_after_cluster_count"`
			DrainNewClusters []struct {
				Template  string `json:"template"`
				FirstLine int    `json:"first_line"`
				Count     int    `json:"count"`
			} `json:"drain_new_clusters"`
			BaselineBeforeDistinct int `json:"baseline_before_distinct"`
			BaselineAfterDistinct  int `json:"baseline_after_distinct"`
		}
		readJSONResult(t, filepath.Join(ws, "drain3_result.json"), &res)
		if res.DrainBeforeCount != 3 || res.DrainAfterCount != 4 {
			t.Fatalf("drain3 cluster counts = before:%d after:%d, want 3 and 4", res.DrainBeforeCount, res.DrainAfterCount)
		}
		if len(res.DrainNewClusters) != 1 || res.DrainNewClusters[0].FirstLine != 37 || res.DrainNewClusters[0].Count != 1 || !strings.Contains(res.DrainNewClusters[0].Template, "payment_gateway") {
			t.Fatalf("drain3 rare new failure not preserved accurately: %+v", res.DrainNewClusters)
		}
		// Simple digit-only normalization fails to collapse hex keys / req-NNNN IDs, proving Drain3 improvement.
		if res.BaselineBeforeDistinct <= res.DrainBeforeCount {
			t.Fatalf("expected Drain3 (%d) to group better than simple baseline (%d)", res.DrainBeforeCount, res.BaselineBeforeDistinct)
		}
	})

	t.Run("06_parse_typed_fields_and_unmatched_counts", func(t *testing.T) {
		ws := t.TempDir()
		copyPY12Fixture(t, ws, "custom_metrics.log")

		runPY12Code(t, svc, ws, "py12-parse", `
import json
from pathlib import Path
import parse

pat = parse.compile("[{ts}] req={req} status={status:d} elapsed_ms={elapsed:f} route={route}")
matched = []
unmatched_lines = []
for lineno, line in enumerate(Path("custom_metrics.log").read_text(encoding="utf-8").splitlines(), 1):
    m = pat.parse(line)
    if m is None:
        unmatched_lines.append(lineno)
        continue
    assert isinstance(m["status"], int)
    assert isinstance(m["elapsed"], float)
    matched.append({"line": lineno, "req": m["req"], "status": m["status"], "elapsed": m["elapsed"], "route": m["route"]})

out = {
    "matched_count": len(matched),
    "unmatched_lines": unmatched_lines,
    "status_codes": [r["status"] for r in matched],
    "total_elapsed_ms": round(sum(r["elapsed"] for r in matched), 2),
    "max_elapsed_line": max(matched, key=lambda r: r["elapsed"])["line"],
}
Path("parse_result.json").write_text(json.dumps(out), encoding="utf-8")
`, nil)

		var res struct {
			MatchedCount   int     `json:"matched_count"`
			UnmatchedLines []int   `json:"unmatched_lines"`
			StatusCodes    []int   `json:"status_codes"`
			TotalElapsedMs float64 `json:"total_elapsed_ms"`
			MaxElapsedLine int     `json:"max_elapsed_line"`
		}
		readJSONResult(t, filepath.Join(ws, "parse_result.json"), &res)
		if res.MatchedCount != 6 || !slices.Equal(res.UnmatchedLines, []int{4, 7}) {
			t.Fatalf("parse matched/unmatched mismatch: %+v", res)
		}
		if !slices.Equal(res.StatusCodes, []int{200, 201, 404, 502, 200, 200}) || math.Abs(res.TotalElapsedMs-390.0) > 0.01 || res.MaxElapsedLine != 5 {
			t.Fatalf("parse values mismatch: %+v", res)
		}
	})

	t.Run("07_pyelftools_cross_arch_elf_vs_go_debug_elf_oracle", func(t *testing.T) {
		ws := t.TempDir()
		for _, f := range []string{"sample_amd64.elf", "sample_arm64_stripped.elf", "not_an_elf.bin"} {
			copyPY12Fixture(t, ws, f)
		}

		// Independent Go debug/elf host oracle
		amd64EF, err := elf.Open(filepath.Join(ws, "sample_amd64.elf"))
		contractcheck.FailErr(t, "go elf.Open amd64", err)
		defer func() { _ = amd64EF.Close() }()
		arm64EF, err := elf.Open(filepath.Join(ws, "sample_arm64_stripped.elf"))
		contractcheck.FailErr(t, "go elf.Open arm64", err)
		defer func() { _ = arm64EF.Close() }()

		wantAmd64Libs, err := amd64EF.ImportedLibraries()
		contractcheck.FailErr(t, "go amd64 ImportedLibraries", err)
		wantArm64Libs, err := arm64EF.ImportedLibraries()
		contractcheck.FailErr(t, "go arm64 ImportedLibraries", err)

		runPY12Code(t, svc, ws, "py12-pyelftools", `
import json
from pathlib import Path
from elftools.common.exceptions import ELFError
from elftools.elf.elffile import ELFFile
from elftools.elf.dynamic import DynamicSection
from elftools.elf.sections import NoteSection

def inspect_elf(path_str):
    with open(path_str, "rb") as f:
        ef = ELFFile(f)
        needed = []
        build_id = None
        has_symtab = ef.get_section_by_name(".symtab") is not None
        for sec in ef.iter_sections():
            if isinstance(sec, DynamicSection):
                for tag in sec.iter_tags():
                    if tag.entry.d_tag == "DT_NEEDED":
                        needed.append(tag.needed)
            elif isinstance(sec, NoteSection):
                for note in sec.iter_notes():
                    if note["n_type"] == "NT_GNU_BUILD_ID":
                        build_id = note["n_desc"]
        return {
            "machine": ef["e_machine"],
            "needed": needed,
            "build_id": build_id,
            "stripped": not has_symtab,
        }

non_elf_rejected = False
try:
    inspect_elf("not_an_elf.bin")
except ELFError:
    non_elf_rejected = True

out = {
    "amd64": inspect_elf("sample_amd64.elf"),
    "arm64": inspect_elf("sample_arm64_stripped.elf"),
    "non_elf_rejected": non_elf_rejected,
}
Path("elf_result.json").write_text(json.dumps(out), encoding="utf-8")
`, nil)

		var res struct {
			AMD64 struct {
				Machine  string   `json:"machine"`
				Needed   []string `json:"needed"`
				BuildID  string   `json:"build_id"`
				Stripped bool     `json:"stripped"`
			} `json:"amd64"`
			ARM64 struct {
				Machine  string   `json:"machine"`
				Needed   []string `json:"needed"`
				BuildID  string   `json:"build_id"`
				Stripped bool     `json:"stripped"`
			} `json:"arm64"`
			NonELFRejected bool `json:"non_elf_rejected"`
		}
		readJSONResult(t, filepath.Join(ws, "elf_result.json"), &res)
		if amd64EF.Machine != elf.EM_X86_64 || res.AMD64.Machine != "EM_X86_64" || !slices.Equal(res.AMD64.Needed, wantAmd64Libs) || res.AMD64.BuildID != "deadbeefcafebabe0102030405060708" || res.AMD64.Stripped {
			t.Fatalf("pyelftools amd64 mismatch vs Go debug/elf oracle: %+v (want libs %v)", res.AMD64, wantAmd64Libs)
		}
		if arm64EF.Machine != elf.EM_AARCH64 || res.ARM64.Machine != "EM_AARCH64" || !slices.Equal(res.ARM64.Needed, wantArm64Libs) || res.ARM64.BuildID != "" || !res.ARM64.Stripped {
			t.Fatalf("pyelftools arm64 mismatch vs Go debug/elf oracle: %+v (want libs %v)", res.ARM64, wantArm64Libs)
		}
		if !res.NonELFRejected {
			t.Fatalf("pyelftools failed to reject non-ELF input")
		}
	})

	t.Run("08_existing_capabilities_har_sqlite_and_rotated_logs", func(t *testing.T) {
		ws := t.TempDir()
		for _, f := range []string{"sample.har", "saved_events.sqlite", "app.log.1.gz", "app.log.2.bz2", "app.log.3.xz", "truncated.log.gz", "truncated.log.bz2", "expand_bomb.log.gz"} {
			copyPY12Fixture(t, ws, f)
		}

		// Record SQLite SHA-256 before guest query
		dbBeforeHash := py12SHA256Hex(py12FixtureBytes(t, "saved_events.sqlite"))

		// Independent Go gzip & bzip2 oracles
		gzR, err := gzip.NewReader(bytes.NewReader(py12FixtureBytes(t, "app.log.1.gz")))
		contractcheck.FailErr(t, "go gzip oracle", err)
		wantGZ, err := io.ReadAll(gzR)
		_ = gzR.Close()
		contractcheck.FailErr(t, "go gzip read", err)
		wantBZ2, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(py12FixtureBytes(t, "app.log.2.bz2"))))
		contractcheck.FailErr(t, "go bzip2 read", err)

		runPY12Code(t, svc, ws, "py12-existing", `
import bz2, gzip, json, sqlite3
from pathlib import Path

# 1. HAR timing & redirects (no double-counting ssl inside connect, no headers/cookies exposed)
har = json.loads(Path("sample.har").read_text(encoding="utf-8"))
har_entries = []
for e in har["log"]["entries"]:
    t = e["timings"]
    # HAR 1.2: negative values mean unavailable; ssl is included in connect when connect >= 0.
    parts = [t.get(k, -1) for k in ("blocked", "dns", "connect", "send", "wait", "receive")]
    non_overlapping_ms = round(sum(v for v in parts if isinstance(v, (int, float)) and v >= 0), 2)
    har_entries.append({
        "status": e["response"]["status"],
        "redirect_url": e["response"].get("redirectURL", ""),
        "non_overlapping_ms": non_overlapping_ms,
        "unavailable_fields": sorted(k for k, v in t.items() if isinstance(v, (int, float)) and v < 0),
    })

# 2. Saved SQLite DB read-only query (mediated file bytes -> in-memory deserialize if needed)
db_mode = "file_ro"
rows = []
try:
    conn = sqlite3.connect("file:saved_events.sqlite?mode=ro", uri=True)
    rows = conn.execute(
        "SELECT s.name, i.severity, i.status_code FROM incidents i JOIN services s ON s.id = i.service_id ORDER BY i.id"
    ).fetchall()
    conn.close()
except sqlite3.Error:
    db_mode = "memory_deserialize"
    conn = sqlite3.connect(":memory:")
    conn.deserialize(Path("saved_events.sqlite").read_bytes())
    rows = conn.execute(
        "SELECT s.name, i.severity, i.status_code FROM incidents i JOIN services s ON s.id = i.service_id ORDER BY i.id"
    ).fetchall()
    conn.close()

# 3. Rotated compressed logs (gzip, bz2, truncated, bounded expansion cap, xz probe)
with gzip.open("app.log.1.gz", "rt", encoding="utf-8") as f:
    gz_text = f.read()
with bz2.open("app.log.2.bz2", "rt", encoding="utf-8") as f:
    bz2_text = f.read()

trunc_gz_err = False
try:
    with gzip.open("truncated.log.gz", "rb") as f:
        f.read()
except (EOFError, OSError, gzip.BadGzipFile):
    trunc_gz_err = True

trunc_bz2_err = False
try:
    with bz2.open("truncated.log.bz2", "rb") as f:
        f.read()
except (EOFError, OSError):
    trunc_bz2_err = True

# Bounded expansion limit (64 KiB cap on 256 KiB compressed fixture)
MAX_EXPAND = 65536
with gzip.open("expand_bomb.log.gz", "rb") as f:
    chunk = f.read(MAX_EXPAND + 1)
    bomb_truncated = len(chunk) > MAX_EXPAND
    bomb_kept = len(chunk[:MAX_EXPAND])

xz_supported = False
xz_text = ""
try:
    import lzma
    with lzma.open("app.log.3.xz", "rt", encoding="utf-8") as f:
        xz_text = f.read()
    xz_supported = True
except Exception:
    xz_supported = False

out = {
    "har_entries": har_entries,
    "db_mode": db_mode,
    "db_rows": rows,
    "gz_text": gz_text,
    "bz2_text": bz2_text,
    "trunc_gz_err": trunc_gz_err,
    "trunc_bz2_err": trunc_bz2_err,
    "bomb_truncated": bomb_truncated,
    "bomb_kept": bomb_kept,
    "xz_supported": xz_supported,
    "xz_text": xz_text,
}
Path("existing_result.json").write_text(json.dumps(out), encoding="utf-8")
`, nil)

		rawOut, err := os.ReadFile(filepath.Join(ws, "existing_result.json"))
		contractcheck.FailErr(t, "read existing_result.json", err)
		for _, secret := range []string{"secret-har-token", "secret-cookie", "secret-set-cookie"} {
			if strings.Contains(string(rawOut), secret) {
				t.Fatalf("HAR header/cookie secret %q leaked into summary output", secret)
			}
		}

		// Verify SQLite source file hash unchanged before & after read-only query.
		dbAfterBytes, err := os.ReadFile(filepath.Join(ws, "saved_events.sqlite"))
		contractcheck.FailErr(t, "read saved_events.sqlite after query", err)
		if got := py12SHA256Hex(dbAfterBytes); got != dbBeforeHash {
			t.Fatalf("saved_events.sqlite mutated during read-only query: before=%s after=%s", dbBeforeHash, got)
		}

		var res struct {
			HAREntries []struct {
				Status            int      `json:"status"`
				RedirectURL       string   `json:"redirect_url"`
				NonOverlappingMs  float64  `json:"non_overlapping_ms"`
				UnavailableFields []string `json:"unavailable_fields"`
			} `json:"har_entries"`
			DBMode        string  `json:"db_mode"`
			DBRows        [][]any `json:"db_rows"`
			GZText        string  `json:"gz_text"`
			BZ2Text       string  `json:"bz2_text"`
			TruncGZErr    bool    `json:"trunc_gz_err"`
			TruncBZ2Err   bool    `json:"trunc_bz2_err"`
			BombTruncated bool    `json:"bomb_truncated"`
			BombKept      int     `json:"bomb_kept"`
			XZSupported   bool    `json:"xz_supported"`
		}
		contractcheck.FailErr(t, "unmarshal existing_result.json", json.Unmarshal(rawOut, &res))
		if len(res.HAREntries) != 3 || math.Abs(res.HAREntries[1].NonOverlappingMs-166.0) > 0.01 || res.HAREntries[0].Status != 302 || res.HAREntries[0].RedirectURL == "" {
			t.Fatalf("HAR timing/redirect mismatch (must not double-count ssl): %+v", res.HAREntries)
		}
		if len(res.DBRows) != 4 || res.DBRows[0][0] != "checkout" {
			t.Fatalf("SQLite joined rows mismatch (mode=%s): %+v", res.DBMode, res.DBRows)
		}
		if res.GZText != string(wantGZ) || res.BZ2Text != string(wantBZ2) {
			t.Fatalf("gzip/bz2 output mismatch vs Go oracles")
		}
		if !res.TruncGZErr || !res.TruncBZ2Err || !res.BombTruncated || res.BombKept != 65536 {
			t.Fatalf("truncated/bounded compression check failed: %+v", res)
		}
		t.Logf("PY12 existing capabilities: sqlite_mode=%s xz_supported=%v", res.DBMode, res.XZSupported)
	})

	t.Run("09_authority_controls_denied_workspace_malformed_and_zero_egress", func(t *testing.T) {
		ws := t.TempDir()
		var trapHits atomic.Int64
		trapSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			trapHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer trapSrv.Close()

		// Write log containing live trap URL — parsing must NEVER visit it.
		logWithTrap := fmt.Sprintf("ERROR upstream=%s status=504\n", trapSrv.URL)
		contractcheck.FailErr(t, "write trap log", os.WriteFile(filepath.Join(ws, "trap.log"), []byte(logWithTrap), 0o644))
		contractcheck.FailErr(t, "write denied file", os.WriteFile(filepath.Join(ws, "secret_denied.txt"), []byte("denied"), 0o644))

		// 1. Parse log containing trap URL -> 0 HTTP hits.
		runPY12Code(t, svc, ws, "py12-zero-egress", `
import parse
from pathlib import Path
pat = parse.compile("ERROR upstream={url} status={status:d}")
m = pat.parse(Path("trap.log").read_text(encoding="utf-8").strip())
assert m is not None and m["status"] == 504
`, nil)
		if trapHits.Load() != 0 {
			t.Fatalf("parsing evidence visited embedded trap URL (%d hits)", trapHits.Load())
		}

		// 2. Denied workspace read -> DecisionDeny recorded, guest fails cleanly.
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		respDenied, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py12-denied-read",
			CallID:       "py12-denied-read-call",
			SessionID:    "py12-controls",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs:      map[string]any{"code": `open("secret_denied.txt", "r").read()`},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Policy: func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
				if op.Target.RelPath == "secret_denied.txt" {
					return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionDeny, Reason: "py12_denied_workspace_read"}
				}
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
			},
		})
		contractcheck.FailErr(t, "execute denied read", err)
		if respDenied.CleanCompletion {
			t.Fatalf("denied workspace read unexpectedly completed cleanly")
		}

		// 3. Recovery: subsequent fresh guest on same Service succeeds cleanly.
		respOK := runPY12Code(t, svc, ws, "py12-recovery", `print("PY12_RECOVERED")`, nil)
		if !strings.Contains(respOK.Stdout, "PY12_RECOVERED") {
			t.Fatalf("post-denial recovery unexpected stdout: %q", respOK.Stdout)
		}
	})
}
