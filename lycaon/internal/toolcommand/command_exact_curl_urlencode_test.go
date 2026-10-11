package toolcommand

import "testing"

func singleReplacement(t *testing.T, command string) (ReplacementCall, bool) {
	t.Helper()
	calls, ok := ExactCommandReplacement(t.Context(), command, "/tmp/project", "")
	if !ok {
		return ReplacementCall{}, false
	}
	if len(calls) != 1 {
		t.Fatalf("%s produced %d calls", command, len(calls))
	}
	return calls[0], true
}

func TestDataURLEncodeMatchesCurlSplitting(t *testing.T) {
	cases := []struct {
		command string
		body    string
	}{
		// A bare operand encodes the whole thing with no name.
		{`curl --data-urlencode "raw content" https://api.test/x`, "raw+content"},
		// A leading '=' means "encode the content, send it without a name".
		{`curl --data-urlencode "=raw content" https://api.test/x`, "raw+content"},
		// A name keeps its bytes; only the content is encoded.
		{`curl --data-urlencode "note=a b&c" https://api.test/x`, "note=a+b%26c"},
	}
	for _, tc := range cases {
		call, ok := singleReplacement(t, tc.command)
		if !ok {
			t.Fatalf("%s was not eligible", tc.command)
		}
		if call.Args["body_text"] != tc.body {
			t.Fatalf("%s -> body_text %q, want %q", tc.command, call.Args["body_text"], tc.body)
		}
	}
}

func TestDataURLEncodeFileFormsStayOnTheCommandBoundary(t *testing.T) {
	// Both '@' forms read a file into the encoded body. A literal operand is a
	// different request, so these are not exact replacements.
	for _, command := range []string{
		`curl --data-urlencode "@payload.txt" https://api.test/x`,
		`curl --data-urlencode "name@payload.txt" https://api.test/x`,
	} {
		if call, ok := singleReplacement(t, command); ok {
			t.Fatalf("%s was translated to %v", command, call.Args)
		}
	}
}

func TestRemoteNameNeedsAFilename(t *testing.T) {
	if call, ok := singleReplacement(t, `curl -O https://a.test/dir/`); ok {
		t.Fatalf("a directory URL produced %v; curl refuses -O here", call.Args)
	}
	if call, ok := singleReplacement(t, `curl -O https://a.test`); ok {
		t.Fatalf("a bare host produced %v", call.Args)
	}
	call, ok := singleReplacement(t, `curl -O https://a.test/dir/file.tar.gz`)
	if !ok || call.Args["response_path"] != "file.tar.gz" {
		t.Fatalf("call = %v ok = %v", call.Args, ok)
	}
}

func TestEnvelopeDeadlineIsCarriedOnlyWhenOneCallCanHoldIt(t *testing.T) {
	one, ok := ExactCommandReplacement(t.Context(), `curl https://a.test/1`, "/tmp/project", "")
	if !ok {
		t.Fatal("single request was not eligible")
	}
	carried, ok := carryCommandEnvelope(Envelope{Command: "x", timeoutMS: 10_000}, one)
	if !ok || carried[0].Args["timeout_ms"] != 10_000 {
		t.Fatalf("carried = %v ok = %v", carried, ok)
	}

	// One runner deadline bounded the whole line. Handing it to each request
	// would let the replacement run for three times what was declared.
	many, ok := ExactCommandReplacement(t.Context(), `curl https://a.test/1; curl https://a.test/2; curl https://a.test/3`, "/tmp/project", "")
	if !ok {
		t.Fatal("sequence was not eligible")
	}
	if _, ok := carryCommandEnvelope(Envelope{Command: "x", timeoutMS: 10_000}, many); ok {
		t.Fatal("a whole-line deadline was spread across a sequence of requests")
	}
	// Without a declared deadline the sequence still translates.
	if _, ok := carryCommandEnvelope(Envelope{Command: "x"}, many); !ok {
		t.Fatal("an undeclared deadline blocked a sequence")
	}
}

func TestEnvelopeDeadlineBelowTheNativeFloorIsRefusedNotWidened(t *testing.T) {
	calls, ok := ExactCommandReplacement(t.Context(), `curl https://a.test/1`, "/tmp/project", "")
	if !ok {
		t.Fatal("request was not eligible")
	}
	if carried, ok := carryCommandEnvelope(Envelope{Command: "x", timeoutMS: 200}, calls); ok {
		t.Fatalf("a 200 ms deadline became %v", carried[0].Args["timeout_ms"])
	}
}

func TestEnvelopeDeadlineKeepsTheTighterBound(t *testing.T) {
	calls, ok := ExactCommandReplacement(t.Context(), `curl -m 5 https://a.test/1`, "/tmp/project", "")
	if !ok {
		t.Fatal("request was not eligible")
	}
	carried, ok := carryCommandEnvelope(Envelope{Command: "x", timeoutMS: 60_000}, calls)
	if !ok || carried[0].Args["timeout_ms"] != 5_000 {
		t.Fatalf("carried = %v", carried[0].Args["timeout_ms"])
	}
}
