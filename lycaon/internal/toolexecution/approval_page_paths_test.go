package toolexecution

import (
 "reflect"
 "testing"
)

func TestPageApprovalUsesMountedDirectory(t *testing.T) {
 for _, tool := range []string{"page_open", "capture_page", "measure_page"} {
  for _, entry := range []string{"", "index.html", "/index.html", "/nested/"} {
   got := filesFromArgs(tool, map[string]any{"project_dir": "site", "path": entry})
   if !reflect.DeepEqual(got, []string{"site"}) {
    t.Errorf("%s entry %q: files = %v, want mounted directory", tool, entry, got)
   }
  }
 }
}
