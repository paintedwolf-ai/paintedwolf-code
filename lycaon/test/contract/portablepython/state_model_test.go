package portablepython

import (
	"bytes"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

func TestReferenceModelRegressionSequences(t *testing.T) {
	t.Parallel()

	t.Run("two successive intentional writes separated by publication advance revision", func(t *testing.T) {
		t.Parallel()
		m := NewReferenceModel(map[string][]byte{"doc.txt": []byte("v1")}, nil)
		h1 := m.Apply(ModelOp{Kind: OpOpen, Path: "doc.txt", Mode: "w"}).HandleID
		m.Apply(ModelOp{Kind: OpWrite, HandleID: h1, Data: []byte("v2")})
		m.Apply(ModelOp{Kind: OpClose, HandleID: h1})
		res1 := m.Apply(ModelOp{Kind: OpApprovalDecision, ReqID: "pub-1", Approved: true})
		if !res1.Published || m.Host.Revisions["doc.txt"] != 2 {
			t.Fatalf("first publication failed: %+v rev=%d", res1, m.Host.Revisions["doc.txt"])
		}

		h2 := m.Apply(ModelOp{Kind: OpOpen, Path: "doc.txt", Mode: "w"}).HandleID
		m.Apply(ModelOp{Kind: OpWrite, HandleID: h2, Data: []byte("v3")})
		m.Apply(ModelOp{Kind: OpClose, HandleID: h2})
		res2 := m.Apply(ModelOp{Kind: OpApprovalDecision, ReqID: "pub-2", Approved: true})
		if !res2.Published || !bytes.Equal(m.Host.Saved["doc.txt"], []byte("v3")) || m.Host.Revisions["doc.txt"] != 3 {
			t.Fatalf("second intentional publication failed: %+v saved=%q rev=%d", res2, m.Host.Saved["doc.txt"], m.Host.Revisions["doc.txt"])
		}
	})

	t.Run("human edit between read and replace preserves human bytes", func(t *testing.T) {
		t.Parallel()
		m := NewReferenceModel(map[string][]byte{"doc.txt": []byte("v1")}, nil)
		_ = m.Apply(ModelOp{Kind: OpRead, Path: "doc.txt"})
		m.Apply(ModelOp{Kind: OpHumanEdit, Path: "doc.txt", Data: []byte("human-v2")})
		h := m.Apply(ModelOp{Kind: OpOpen, Path: "doc.txt", Mode: "w"}).HandleID
		m.Apply(ModelOp{Kind: OpWrite, HandleID: h, Data: []byte("stale-agent-v2")})
		m.Apply(ModelOp{Kind: OpClose, HandleID: h})
		res := m.Apply(ModelOp{Kind: OpApprovalDecision, ReqID: "pub-stale", Approved: true})
		if !res.Conflict {
			t.Fatalf("expected revision conflict, got %+v", res)
		}
		if !bytes.Equal(m.Host.Drafts["doc.txt"], []byte("human-v2")) {
			t.Fatalf("human edit overwritten: got %q", m.Host.Drafts["doc.txt"])
		}
	})

	t.Run("aliases of one document share revision conflict detection", func(t *testing.T) {
		t.Parallel()
		m := NewReferenceModel(
			map[string][]byte{"src/config.toml": []byte("orig")},
			map[string]string{"./src/config.toml": "src/config.toml"},
		)
		_ = m.Apply(ModelOp{Kind: OpRead, Path: "./src/config.toml"})
		m.Apply(ModelOp{Kind: OpHumanEdit, Path: "src/config.toml", Data: []byte("human-edit")})
		h := m.Apply(ModelOp{Kind: OpOpen, Path: "./src/config.toml", Mode: "w"}).HandleID
		m.Apply(ModelOp{Kind: OpWrite, HandleID: h, Data: []byte("agent-edit")})
		m.Apply(ModelOp{Kind: OpClose, HandleID: h})
		res := m.Apply(ModelOp{Kind: OpApprovalDecision, ReqID: "pub-alias", Approved: true})
		if !res.Conflict {
			t.Fatalf("expected conflict across document aliases, got %+v", res)
		}
		if !bytes.Equal(m.Host.Drafts["src/config.toml"], []byte("human-edit")) {
			t.Fatalf("expected human edit preserved, got %q", m.Host.Drafts["src/config.toml"])
		}
	})

	t.Run("existing reader versus reopened reader after buffered write and flush", func(t *testing.T) {
		t.Parallel()
		m := NewReferenceModel(map[string][]byte{"buf.txt": []byte("initial")}, nil)
		rExisting := m.Apply(ModelOp{Kind: OpOpen, Path: "buf.txt", Mode: "r"}).HandleID
		w := m.Apply(ModelOp{Kind: OpOpen, Path: "buf.txt", Mode: "w"}).HandleID
		m.Apply(ModelOp{Kind: OpWrite, HandleID: w, Data: []byte("staged-prefix")})
		m.Apply(ModelOp{Kind: OpFlush, HandleID: w})

		existingData := m.Apply(ModelOp{Kind: OpRead, HandleID: rExisting}).Data
		rReopened := m.Apply(ModelOp{Kind: OpOpen, Path: "buf.txt", Mode: "r"}).HandleID
		reopenedData := m.Apply(ModelOp{Kind: OpRead, HandleID: rReopened}).Data

		if !bytes.Equal(existingData, []byte("initial")) {
			t.Fatalf("existing reader saw %q, want initial", existingData)
		}
		if !bytes.Equal(reopenedData, []byte("staged-prefix")) {
			t.Fatalf("reopened reader saw %q, want staged-prefix", reopenedData)
		}
		if !bytes.Equal(m.Host.Saved["buf.txt"], []byte("initial")) {
			t.Fatalf("host disk mutated before settlement: %q", m.Host.Saved["buf.txt"])
		}
	})

	t.Run("partial success followed by refusal latches stop against cleanup writes", func(t *testing.T) {
		t.Parallel()
		m := NewReferenceModel(map[string][]byte{"a.txt": []byte("a0"), "b.txt": []byte("b0")}, nil)
		h1 := m.Apply(ModelOp{Kind: OpOpen, Path: "a.txt", Mode: "w"}).HandleID
		m.Apply(ModelOp{Kind: OpWrite, HandleID: h1, Data: []byte("a1")})
		m.Apply(ModelOp{Kind: OpClose, HandleID: h1})
		if !m.Apply(ModelOp{Kind: OpApprovalDecision, ReqID: "pub-a", Approved: true}).Published {
			t.Fatal("expected first commit to publish")
		}

		h2 := m.Apply(ModelOp{Kind: OpOpen, Path: "b.txt", Mode: "w"}).HandleID
		m.Apply(ModelOp{Kind: OpWrite, HandleID: h2, Data: []byte("b1")})
		m.Apply(ModelOp{Kind: OpClose, HandleID: h2})
		declined := m.Apply(ModelOp{Kind: OpApprovalDecision, ReqID: "pub-b", Approved: false})
		if !declined.Rejected {
			t.Fatalf("expected declined approval to reject, got %+v", declined)
		}
		cleanupOpen := m.Apply(ModelOp{Kind: OpOpen, Path: "b.txt", Mode: "w"})
		if !cleanupOpen.Rejected {
			t.Fatalf("expected latched stop to block cleanup open, got %+v", cleanupOpen)
		}
		if !bytes.Equal(m.Host.Saved["a.txt"], []byte("a1")) || !bytes.Equal(m.Host.Saved["b.txt"], []byte("b0")) {
			t.Fatalf("unexpected host state after decline: a=%q b=%q", m.Host.Saved["a.txt"], m.Host.Saved["b.txt"])
		}
	})

	t.Run("host restart while approval pending prevents stale publication and replay", func(t *testing.T) {
		t.Parallel()
		m := NewReferenceModel(map[string][]byte{"doc.txt": []byte("v1")}, nil)
		h := m.Apply(ModelOp{Kind: OpOpen, Path: "doc.txt", Mode: "w"}).HandleID
		m.Apply(ModelOp{Kind: OpWrite, HandleID: h, Data: []byte("v2")})
		m.Apply(ModelOp{Kind: OpClose, HandleID: h})
		m.Apply(ModelOp{Kind: OpRestart})
		stale := m.Apply(ModelOp{Kind: OpApprovalDecision, ReqID: "pub-after-restart", Approved: true})
		if !stale.Rejected || stale.Published {
			t.Fatalf("stale approval after restart must be rejected, got %+v", stale)
		}
		if !bytes.Equal(m.Host.Saved["doc.txt"], []byte("v1")) {
			t.Fatalf("saved bytes mutated after restart: %q", m.Host.Saved["doc.txt"])
		}
	})
}

func TestReferenceModelGeneratedOperationSequences(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		m := NewReferenceModel(
			map[string][]byte{
				"a.txt": []byte("init-a"),
				"b.txt": []byte("init-b"),
			},
			map[string]string{
				"./a.txt": "a.txt",
			},
		)
		paths := []string{"a.txt", "./a.txt", "b.txt", "tmp.txt"}
		openHandles := make([]int, 0)
		steps := rapid.IntRange(4, 24).Draw(rt, "steps")
		reqCounter := 0

		for i := 0; i < steps; i++ {
			opIdx := rapid.IntRange(0, 9).Draw(rt, fmt.Sprintf("op_%d", i))
			p := paths[rapid.IntRange(0, len(paths)-1).Draw(rt, fmt.Sprintf("path_%d", i))]
			payload := []byte(fmt.Sprintf("step-%d", i))

			prevSavedA := cloneBytes(m.Host.Saved["a.txt"])
			prevSavedB := cloneBytes(m.Host.Saved["b.txt"])
			prevHistLen := len(m.History)

			var op ModelOp
			switch opIdx {
			case 0:
				mode := "r"
				if rapid.Bool().Draw(rt, fmt.Sprintf("write_mode_%d", i)) {
					mode = "w"
				}
				op = ModelOp{Kind: OpOpen, Path: p, Mode: mode}
			case 1:
				hid := 0
				if len(openHandles) > 0 && rapid.Bool().Draw(rt, fmt.Sprintf("use_handle_%d", i)) {
					hid = openHandles[rapid.IntRange(0, len(openHandles)-1).Draw(rt, fmt.Sprintf("hid_%d", i))]
				}
				op = ModelOp{Kind: OpRead, Path: p, HandleID: hid}
			case 2:
				hid := 0
				if len(openHandles) > 0 {
					hid = openHandles[rapid.IntRange(0, len(openHandles)-1).Draw(rt, fmt.Sprintf("whid_%d", i))]
				}
				op = ModelOp{Kind: OpWrite, HandleID: hid, Data: payload}
			case 3:
				hid := 0
				if len(openHandles) > 0 {
					hid = openHandles[rapid.IntRange(0, len(openHandles)-1).Draw(rt, fmt.Sprintf("fhid_%d", i))]
				}
				op = ModelOp{Kind: OpFlush, HandleID: hid}
			case 4:
				hid := 0
				if len(openHandles) > 0 {
					hid = openHandles[rapid.IntRange(0, len(openHandles)-1).Draw(rt, fmt.Sprintf("chid_%d", i))]
				}
				op = ModelOp{Kind: OpClose, HandleID: hid}
			case 5:
				dst := paths[rapid.IntRange(0, len(paths)-1).Draw(rt, fmt.Sprintf("dst_%d", i))]
				op = ModelOp{Kind: OpRename, Path: p, DestPath: dst}
			case 6:
				op = ModelOp{Kind: OpHumanEdit, Path: p, Data: payload}
			case 7:
				reqCounter++
				reqID := fmt.Sprintf("req-%d", reqCounter)
				if reqCounter > 1 && rapid.Bool().Draw(rt, fmt.Sprintf("replay_req_%d", i)) {
					reqID = fmt.Sprintf("req-%d", rapid.IntRange(1, reqCounter-1).Draw(rt, fmt.Sprintf("old_req_%d", i)))
				}
				op = ModelOp{
					Kind:     OpApprovalDecision,
					ReqID:    reqID,
					Approved: rapid.Bool().Draw(rt, fmt.Sprintf("approved_%d", i)),
				}
			case 8:
				op = ModelOp{Kind: OpInterrupt}
			case 9:
				op = ModelOp{Kind: OpRestart}
			}

			res := m.Apply(op)
			if op.Kind == OpOpen && !res.Rejected && res.HandleID > 0 {
				openHandles = append(openHandles, res.HandleID)
			}

			// Invariant 1: Saved host bytes never change except on an approved publication.
			if op.Kind != OpApprovalDecision || !res.Published {
				if !bytes.Equal(prevSavedA, m.Host.Saved["a.txt"]) || !bytes.Equal(prevSavedB, m.Host.Saved["b.txt"]) {
					rt.Fatalf("host saved bytes mutated outside publication on op %+v", op)
				}
				if len(m.History) != prevHistLen {
					rt.Fatalf("history grew without publication on op %+v", op)
				}
			}

			// Invariant 2: Exactly-once publication per (ReqID, Path).
			seen := make(map[string]bool, len(m.History))
			for _, ev := range m.History {
				key := ev.ReqID + "\x00" + ev.Path
				if seen[key] {
					rt.Fatalf("duplicate publication recorded for %s", key)
				}
				seen[key] = true
			}
		}
	})
}
