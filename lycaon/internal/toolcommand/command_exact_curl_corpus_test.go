package toolcommand

import "testing"

// Compound requests redirect only when every operation has a native equivalent.
func TestExactCommandReplacementObservedCurlCorpus(t *testing.T) {
	representable := []struct {
		command string
		calls   int
	}{
		{"curl -sS -o /dev/null -w %{http_code} http://localhost:5555/; echo; curl -sS -o /dev/null -w %{http_code} http://localhost:5555/api/v1/index/config.json", 2},
		{"curl -sS -X POST http://localhost:5555/api/v1/auth -H Content-Type:application/json -d {\"user\":\"u\",\"pwd\":\"p\"}", 1},
		{"curl -sS -X POST http://localhost:5555/api/v1/login -H Content-Type:application/json -d {\"user\":\"u\",\"pwd\":\"p\"}; echo; curl -sS -X POST http://localhost:5555/api/v1/session -H Content-Type:application/json -d {\"user\":\"u\",\"pwd\":\"p\"}", 2},
		{"curl -sS -X PUT http://localhost:5555/api/v1/auth -H Content-Type:application/json -d {\"user\":\"u\",\"pwd\":\"p\"}; echo; curl -sS -X PATCH http://localhost:5555/api/v1/auth -H Content-Type:application/json -d {\"user\":\"u\",\"pwd\":\"p\"}", 2},
		{"curl -sS http://localhost:5555/api/v1/auth/session -d {\"user\":\"u\",\"pwd\":\"p\"} -H Content-Type:application/json", 1},
		{"curl -sS https://api.github.com/repos/example/example/git/trees/main?recursive=1 -o tmp/tree.json", 1},
		{"curl -sS https://api.github.com/repos/example/example/git/trees/main?recursive=1 -o /tmp/tree.json", 1},
		{"curl -sS -X GET http://localhost:5555/api/v1/auth -H Content-Type:application/json; echo; curl -sS -X OPTIONS -i http://localhost:5555/api/v1/auth", 2},
		{"curl -sS https://raw.githubusercontent.com/example/example/main/CONTRIBUTING.md -o tmp/contrib.md", 1},
		{"curl -sS https://raw.githubusercontent.com/example/example/main/ui/src/services/routes.ts -o tmp/routes.ts; echo; curl -sS https://raw.githubusercontent.com/example/example/main/ui/src/services/urls.ts -o tmp/urls.ts", 2},
		{"curl -sS -o /dev/null -w a:%{http_code} -X POST http://localhost:5555/api/v1/login; echo; curl -sS -o /dev/null -w b:%{http_code} -X POST http://localhost:5555/api/v1/auth/login", 2},
		{"curl -sS http://localhost:5555/assets/index.js -o tmp/ui.js", 1},
		{"curl -sS -X POST http://localhost:5555/api/v1/user/login -H Content-Type:application/json -d \"{\\\"user\\\":\\\"u\\\",\\\"pwd\\\":\\\"p\\\"}\"", 1},
		{"curl -sS -c /tmp/jar.txt -X POST http://localhost:5555/api/v1/user/login -H Content-Type:application/json -d \"{\\\"user\\\":\\\"u\\\"}\"; echo; curl -sS -b /tmp/jar.txt http://localhost:5555/api/v1/user", 2},
		{"curl -sS -b /tmp/jar.txt -X POST http://localhost:5555/api/v1/user/add_token -H Content-Type:application/json -d \"{\\\"name\\\":\\\"ci\\\"}\"", 1},
	}
	for _, tc := range representable {
		t.Run(tc.command, func(t *testing.T) {
			calls, ok := ExactCommandReplacement(t.Context(), tc.command, t.TempDir(), "")
			if !ok || len(calls) != tc.calls {
				t.Fatalf("replacement = %#v ok=%v, want %d http_request calls", calls, ok, tc.calls)
			}
			for _, call := range calls {
				if call.Tool != "http_request" {
					t.Fatalf("call %#v is not http_request", call)
				}
			}
		})
	}

	staysCommand := []struct {
		command string
		reason  string
	}{
		{"curl -sS https://raw.githubusercontent.com/example/example/main/common/src/settings.rs -o tmp/settings.rs; grep -n \"admin\" tmp/settings.rs | head -20", "a pipeline follows the request"},
		{"mkdir -p registry/data && docker run -d --name registry -p 4873:4873 example/registry", "a process spawn follows the request"},
	}
	for _, tc := range staysCommand {
		t.Run(tc.command, func(t *testing.T) {
			if calls, ok := ExactCommandReplacement(t.Context(), tc.command, t.TempDir(), ""); ok {
				t.Fatalf("%s: redirected to %#v", tc.reason, calls)
			}
		})
	}
}
