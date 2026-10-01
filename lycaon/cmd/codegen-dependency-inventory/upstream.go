package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/module"
)

// snapshot is the checked-in record of upstream versions.
type snapshot struct {
	Fetched  string            `json:"fetched"`
	Versions map[string]string `json:"versions"`
}

func loadSnapshot(path string) (snapshot, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed repository path
	if err != nil {
		return snapshot{}, fmt.Errorf("read %s: %w (run ./task codegen:dependency-inventory:upstream)", path, err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return snapshot{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return snap, nil
}

func (s snapshot) encode() ([]byte, error) {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}
	return append(body, '\n'), nil
}

const fetchWorkers = 8

type fetcher struct {
	client *http.Client
	token  string
}

// refreshSnapshot queries every upstream source. Any failure aborts the
// refresh so a partial snapshot is never written.
func refreshSnapshot(ctx context.Context, sections []section, now time.Time) (snapshot, error) {
	type job struct {
		ref upstreamRef
		pin string
	}
	jobs := map[string]job{}
	for _, s := range sections {
		for _, r := range s.rows {
			for i, ref := range r.upstream {
				jobs[ref.key] = job{ref: ref, pin: r.comparablePin(i)}
			}
		}
	}
	f := fetcher{client: &http.Client{Timeout: 30 * time.Second}, token: os.Getenv("GITHUB_TOKEN")}
	snap := snapshot{Fetched: now.UTC().Format("2006-01-02"), Versions: map[string]string{}}
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, fetchWorkers)
	)
	for key, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			v, err := f.fetch(ctx, j.ref.source, j.pin)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
				return
			}
			snap.Versions[key] = v
		}()
	}
	wg.Wait()
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return snap, errors.Join(errs...)
}

func (f fetcher) fetch(ctx context.Context, ref sourceRef, pin string) (string, error) {
	switch ref.Kind {
	case kindGoModule:
		escaped, err := module.EscapePath(ref.Name)
		if err != nil {
			return "", fmt.Errorf("escape module path: %w", err)
		}
		return f.versionField(ctx, "https://proxy.golang.org/"+escaped+"/@latest")
	case kindNPM:
		return f.versionField(ctx, "https://registry.npmjs.org/"+ref.Name+"/latest")
	case kindCrate:
		return f.crate(ctx, ref.Name)
	case "github-release":
		var out struct {
			TagName string `json:"tag_name"`
		}
		if err := f.getJSON(ctx, "https://api.github.com/repos/"+ref.Repo+"/releases/latest", &out); err != nil {
			return "", err
		}
		return strip(out.TagName, ref.Strip), nil
	case "github-tag":
		return f.githubTag(ctx, ref)
	case "github-commit":
		var out []struct{ SHA string }
		if err := f.getJSON(ctx, "https://api.github.com/repos/"+ref.Repo+"/commits?per_page=1", &out); err != nil || len(out) == 0 {
			return "", errors.Join(err, errors.New("no commits"))
		}
		return out[0].SHA, nil
	case "go-release":
		return f.goRelease(ctx, ref.Track, pin)
	case "node-release":
		return f.nodeRelease(ctx, pin)
	case "rust-stable":
		return f.rustStable(ctx)
	case "chrome-for-testing":
		var out struct {
			Channels map[string]struct{ Version string } `json:"channels"`
		}
		url := "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions.json"
		if err := f.getJSON(ctx, url, &out); err != nil {
			return "", err
		}
		return out.Channels[ref.Channel].Version, nil
	}
	return "", fmt.Errorf("unsupported upstream kind %q", ref.Kind)
}

func strip(tag, prefix string) string {
	if prefix == "" {
		prefix = "v"
	}
	return strings.TrimPrefix(tag, prefix)
}

func (f fetcher) get(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("request %s: %w", url, err)
		}
		req.Header.Set("User-Agent", "paintedwolf-dependency-inventory")
		if f.token != "" && strings.HasPrefix(url, "https://api.github.com/") {
			req.Header.Set("Authorization", "Bearer "+f.token)
		}
		resp, err := f.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("get %s: %w", url, err)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		switch {
		case err != nil:
			lastErr = fmt.Errorf("read %s: %w", url, err)
		case resp.StatusCode != http.StatusOK:
			lastErr = fmt.Errorf("get %s: %s", url, resp.Status)
		default:
			return body, nil
		}
	}
	return nil, lastErr
}

func (f fetcher) versionField(ctx context.Context, url string) (string, error) {
	var out struct{ Version string }
	if err := f.getJSON(ctx, url, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

func (f fetcher) getJSON(ctx context.Context, url string, out any) error {
	body, err := f.get(ctx, url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

// crate reads the sparse index, which carries no crawler rate limit.
func (f fetcher) crate(ctx context.Context, name string) (string, error) {
	lower := strings.ToLower(name)
	var prefix string
	switch len(lower) {
	case 1:
		prefix = "1"
	case 2:
		prefix = "2"
	case 3:
		prefix = "3/" + lower[:1]
	default:
		prefix = lower[:2] + "/" + lower[2:4]
	}
	body, err := f.get(ctx, "https://index.crates.io/"+prefix+"/"+lower)
	if err != nil {
		return "", err
	}
	var best string
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		var entry struct {
			Vers   string `json:"vers"`
			Yanked bool   `json:"yanked"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Yanked || strings.Contains(entry.Vers, "-") {
			continue
		}
		if best == "" || compareVersions(entry.Vers, best) > 0 {
			best = entry.Vers
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan crate index for %s: %w", name, err)
	}
	return best, nil
}

func (f fetcher) githubTag(ctx context.Context, ref sourceRef) (string, error) {
	pattern, err := regexp.Compile(ref.Pattern)
	if err != nil {
		return "", fmt.Errorf("tag pattern: %w", err)
	}
	var tags []struct{ Name string }
	if err := f.getJSON(ctx, "https://api.github.com/repos/"+ref.Repo+"/tags?per_page=100", &tags); err != nil {
		return "", err
	}
	var best string
	for _, t := range tags {
		if pattern.MatchString(t.Name) && (best == "" || compareVersions(t.Name, best) > 0) {
			best = t.Name
		}
	}
	if best == "" {
		return "", fmt.Errorf("no tag of %s matches %s", ref.Repo, ref.Pattern)
	}
	return strip(best, ref.Strip), nil
}

// goRelease returns the newest stable Go, or with track "line" the newest
// point release of the pinned minor line.
func (f fetcher) goRelease(ctx context.Context, track, pin string) (string, error) {
	var releases []struct {
		Version string
		Stable  bool
	}
	if err := f.getJSON(ctx, "https://go.dev/dl/?mode=json&include=all", &releases); err != nil {
		return "", err
	}
	line := ""
	if track == "line" {
		fields := strings.SplitN(pin, ".", 3)
		if len(fields) < 2 {
			return "", fmt.Errorf("pin %q has no minor line", pin)
		}
		line = fields[0] + "." + fields[1] + "."
	}
	var best string
	for _, r := range releases {
		v := strings.TrimPrefix(r.Version, "go")
		if r.Stable && strings.HasPrefix(v, line) && (best == "" || compareVersions(v, best) > 0) {
			best = v
		}
	}
	return best, nil
}

// nodeRelease returns the newest release in the pinned major line.
func (f fetcher) nodeRelease(ctx context.Context, pin string) (string, error) {
	var releases []struct{ Version string }
	if err := f.getJSON(ctx, "https://nodejs.org/dist/index.json", &releases); err != nil {
		return "", err
	}
	major := "v" + strings.SplitN(pin, ".", 2)[0] + "."
	var best string
	for _, r := range releases {
		if strings.HasPrefix(r.Version, major) && (best == "" || compareVersions(r.Version, best) > 0) {
			best = r.Version
		}
	}
	return strings.TrimPrefix(best, "v"), nil
}

func (f fetcher) rustStable(ctx context.Context) (string, error) {
	body, err := f.get(ctx, "https://static.rust-lang.org/dist/channel-rust-stable.toml")
	if err != nil {
		return "", err
	}
	var channel struct {
		Pkg map[string]struct{ Version string } `toml:"pkg"`
	}
	if err := toml.Unmarshal(body, &channel); err != nil {
		return "", fmt.Errorf("decode rust channel: %w", err)
	}
	fields := strings.Fields(channel.Pkg["rust"].Version)
	if len(fields) == 0 {
		return "", errors.New("rust channel has no rust package version")
	}
	return fields[0], nil
}
