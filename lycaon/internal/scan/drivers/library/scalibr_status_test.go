package library

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/osv-scalibr/plugin"
	scalibrresult "github.com/google/osv-scalibr/result"
)

func TestValidateScalibrResultRejectsIncompleteScan(t *testing.T) {
	result := &scalibrresult.ScanResult{Status: &plugin.ScanStatus{
		Status: plugin.ScanStatusPartiallySucceeded, FailureReason: "vulnerability database unavailable",
	}}
	if err := validateScalibrResult(result); err == nil || !strings.Contains(err.Error(), "vulnerability database unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateScalibrResultRejectsFailedPlugin(t *testing.T) {
	result := &scalibrresult.ScanResult{
		Status: &plugin.ScanStatus{Status: plugin.ScanStatusSucceeded},
		PluginStatus: []*plugin.Status{{Name: "vulnmatch/osvlocal", Status: &plugin.ScanStatus{
			Status: plugin.ScanStatusFailed, FailureReason: "database corrupt",
		}}},
	}
	if err := validateScalibrResult(result); err == nil || !strings.Contains(err.Error(), "vulnmatch/osvlocal") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateScalibrResultAcceptsCompleteScan(t *testing.T) {
	result := &scalibrresult.ScanResult{
		Status: &plugin.ScanStatus{Status: plugin.ScanStatusSucceeded},
		PluginStatus: []*plugin.Status{{Name: "vulnmatch/osvlocal", Status: &plugin.ScanStatus{
			Status: plugin.ScanStatusSucceeded,
		}}},
	}
	if err := validateScalibrResult(result); err != nil {
		t.Fatalf("validateScalibrResult: %v", err)
	}
}

func TestValidateScalibrPartialScanPreservesFailedPlugin(t *testing.T) {
	result := &scalibrresult.ScanResult{
		Status: &plugin.ScanStatus{Status: plugin.ScanStatusPartiallySucceeded, FailureReason: "not all plugins succeeded"},
		PluginStatus: []*plugin.Status{
			{Name: "extractor", Status: &plugin.ScanStatus{Status: plugin.ScanStatusSucceeded}},
			{Name: "vulnmatch/osvlocal", Status: &plugin.ScanStatus{Status: plugin.ScanStatusFailed, FailureReason: "database unavailable"}},
		},
	}
	err := validateScalibrResult(result)
	var failure *scalibrPluginFailure
	if !errors.As(err, &failure) || failure.name != "vulnmatch/osvlocal" || failure.status != int(plugin.ScanStatusFailed) || failure.reason != "database unavailable" {
		t.Fatalf("partial scan lost structured plugin failure: %v", err)
	}
	if !strings.Contains(err.Error(), "not all plugins succeeded") {
		t.Fatalf("partial scan lost aggregate failure: %v", err)
	}
}
