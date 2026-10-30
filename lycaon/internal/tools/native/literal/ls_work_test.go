package literal

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"testing"
)

type listingWorkLog struct{ records map[string]map[string]int64 }

func (l *listingWorkLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *listingWorkLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *listingWorkLog) WithGroup(string) slog.Handler            { return l }
func (l *listingWorkLog) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "command directory listing completed" {
		return nil
	}
	execution := ""
	work := map[string]int64{}
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "execution_id" {
			execution = attr.Value.String()
		}
		if attr.Value.Kind() == slog.KindInt64 {
			work[attr.Key] = attr.Value.Int64()
		}
		return true
	})
	l.records[execution] = work
	return nil
}

func listingLogs(t *testing.T) *listingWorkLog {
	t.Helper()
	logs := &listingWorkLog{records: map[string]map[string]int64{}}
	old := slog.Default()
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(old) })
	return logs
}

func TestListingWorkFollowsSelectedOutput(t *testing.T) {
	logs := listingLogs(t)
	files := map[string]string{"list/sub/": "", "list/.hidden": "hidden"}
	for i := range 80 {
		files[fmt.Sprintf("list/file-%02d", i)] = "data"
	}
	h := newHarness(t, files)
	for _, flags := range [][]string{nil, {"-a"}, {"-A"}, {"-r"}, {"-m"}, {"-f"}, {"-U"}, {"-L"}, {"-P"}, {"-h"}, {"--color=never"}} {
		args := append(append([]string{"ls"}, flags...), "list")
		result := h.exec("", args...)
		if result.err != nil || result.result.Status != 0 {
			t.Fatalf("%v: result=%+v err=%v", args, result.result, result.err)
		}
		work := logs.records[result.execution]
		if work["entries"] < 81 || work["entry_metadata_reads"] != 0 {
			t.Fatalf("%v fetched unselected metadata: %v", args, work)
		}
	}
	for _, flags := range [][]string{{"-l"}, {"-t"}, {"-S"}, {"-i"}, {"-s"}, {"-F"}, {"-p"}, {"-R"}, {"--group-directories-first"}, {"-c"}, {"-u"}} {
		args := append(append([]string{"ls"}, flags...), "list")
		result := h.exec("", args...)
		if result.err != nil || result.result.Status != 0 {
			t.Fatalf("%v: result=%+v err=%v", args, result.result, result.err)
		}
		if work := logs.records[result.execution]; work["entry_metadata_reads"] < 81 {
			t.Fatalf("%v omitted required metadata: %v", args, work)
		}
	}
}

func TestListingBoundsSelectedMembershipBeforeMetadata(t *testing.T) {
	logs := listingLogs(t)
	files := map[string]string{"list/a": "", "list/b": "", "list/c": "", "list/d": ""}
	for i := range 200 {
		files[fmt.Sprintf("list/.hidden-%03d", i)] = ""
	}
	h := newHarness(t, files)
	h.owners.catalog.Language.Limits = slices.Clone(h.owners.catalog.Language.Limits)
	for i := range h.owners.catalog.Language.Limits {
		if h.owners.catalog.Language.Limits[i].ID == "traversal_visits" {
			h.owners.catalog.Language.Limits[i].Maximum = 4
		}
	}
	h.exec("", "ls", "list").expect(t, "visible bounded listing", "a\nb\nc\nd\n", "", 0)
	for _, flag := range []string{"-A", "-a", "-lA"} {
		r := h.exec("", "ls", flag, "list")
		r.refused(t, "bounded membership", "COMMAND_LIMIT_EXCEEDED")
		if work := logs.records[r.execution]; work["entry_metadata_reads"] != 0 {
			t.Fatalf("%s read metadata before bounding membership: %v", flag, work)
		}
	}
}

func TestListingNameSelectionPreservesLinkAndParentAuthority(t *testing.T) {
	h := newHarness(t, map[string]string{"list/file": "", "list/nested/": ""})
	symlink(t, "/outside/unavailable", h.root+"/list/link")
	h.exec("", "ls", "list").expect(t, "names do not follow links", "file\nlink\nnested\n", "", 0)
	r := h.exec("", "ls", "-a", "list")
	r.expect(t, "admitted parent name", ".\n..\nfile\nlink\nnested\n", "", 0)
}

func TestListingWorkCountsFollowedLinkMetadata(t *testing.T) {
	logs := listingLogs(t)
	h := newHarness(t, map[string]string{"list/file": "data"})
	symlink(t, "file", h.root+"/list/link")
	for _, tc := range []struct {
		args  []string
		reads int64
	}{{[]string{"ls", "list"}, 0}, {[]string{"ls", "-l", "list"}, 2}, {[]string{"ls", "-lL", "list"}, 3}} {
		result := h.exec("", tc.args...)
		if result.err != nil || result.result.Status != 0 {
			t.Fatalf("%v result=%+v error=%v", tc.args, result.result, result.err)
		}
		if work := logs.records[result.execution]; work["entry_metadata_reads"] != tc.reads {
			t.Fatalf("%v work=%v want reads=%d", tc.args, work, tc.reads)
		}
	}
}
