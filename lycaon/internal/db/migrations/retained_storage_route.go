package migrations

import sqlite3 "modernc.org/sqlite/lib"

// BoundedRetainedStorage recognizes exactly the route whose indexes and
// sorter inputs were reviewed. A future step keeps conservative admission
// until its physical work has been included in that proof.
func (p Plan) BoundedRetainedStorage() bool {
	return p.retainedStoragePrepared && p.reviewedRetainedRoute()
}

func (p Plan) reviewedRetainedRoute() bool {
	const sourceID = "2026-10-16 14:45:20 555e07ef33f69a0957d24d8e555b702464a131d6f1607f4c3c1e4551e8e6ca76"
	if sqlite3.SQLITE_SOURCE_ID != sourceID || p.Target != (Baseline{Revision: 11, Shape: "3a065b2adccf2628ce0e203a4e04962c974b462a7cae85e8996ba4e7696fe9e6"}) {
		return false
	}
	if p.Source != manifestLayoutSource || p.CopyPlan() == nil {
		return false
	}
	reviewed := [][2]string{
		{"source-manifest-layout", "fc0cbdf1f48df9fd7730f8f35db355d2b32e2e687e80d6ec9eff09258a9b221c"},
		{"vault-unlock-away", "13094df267f14b2a7cda143ef5ac6466bc242fb65f5f5452c6e3554e285d2676"},
		{"native-child-once-authority", "3c3862c529179e69f83a21dbd3b6d6bdf89017b92ee37bf90aa09fa39e6b6d58"},
		{"portable-program-receipts", "bde8b3115696ead32e21b55f520fff50c94e04da79604a0a71a6e7889370ef40"},
		{"portable-program-native-gap", "f19c6276d3a1d4e7693db5fcb5d4261d46c3033b2878117c06e0f04b55998540"},
		{"portable-program-inline-inputs", "25c49ce1ad22f336ff4f77dac47f314e36bd733c1594712aa10c949bcef38dba"},
		{"portable-source-link-kind", "280d02156ff51fb374e9babb5429f5e2075a70fe9f58faaa244f83609667cb41"},
	}
	if len(p.steps) != len(reviewed) {
		return false
	}
	for i, step := range p.steps {
		if step.ID != reviewed[i][0] {
			return false
		}
		if step.Checksum != reviewed[i][1] {
			switch step.ID {
			case "native-child-once-authority":
				// Decoder dispatch changes no SQL, indexes or allocation work.
				if step.Checksum != "13848cc7b59739e3971bd0a793d996372bde110f0b6b4e9bb14a9925697557a5" &&
					step.Checksum != "417303ae6c704fdd6909312f57a37c41428182ced025d74d2c0cd4123fc89f95" {
					return false
				}
			case "source-manifest-layout":
				// The reviewed 64-row VALUES input streams through the unchanged
				// fixed CHECK set; reads, sorts, rows and identities are unchanged.
				if step.Checksum != "046635199cf698cecc77b7f59fe03b92a9874b1b1789025472f44e0af6b8931e" &&
					step.Checksum != "3af0f1899c7ec5e85633c260f16d75af8ed506f0cd888e60fd096dcfc54e91d3" {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
