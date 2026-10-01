package tsparse

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/odvcencio/gotreesitter/grammars"
)

func TestConfiguredTimeoutsAndLiveCorrection(t *testing.T) {
	fsys := fstest.MapFS{string(config.SourceParsing): &fstest.MapFile{Data: []byte("version: 1\nvalidation_timeout_ms: 45000\nanalysis_timeout_ms: 1\n")}}
	configtest.Use(t, fsys)
	cfg, err := LoadConfig()
	testutil.FailErr(t, "load parsing configuration", err)
	if got, _ := cfg.timeout(Validation); got != 45*time.Second {
		t.Fatalf("validation timeout = %v", got)
	}
	lang := grammars.DetectLanguageByName("bash").Language()
	tree, err := Parse(context.Background(), lang, pathologicalBash(160), Analysis)
	var failure *Failure
	if tree != nil || !errors.As(err, &failure) || failure.TimeoutMS != 1 {
		t.Fatalf("configured parse = %v, %v", tree, err)
	}
	fsys[string(config.SourceParsing)].Data = []byte("version: 1\nvalidation_timeout_ms: 60000\nanalysis_timeout_ms: 10000\n")
	cfg, err = LoadConfig()
	if err != nil || cfg.ValidationTimeoutMS != 60000 || cfg.AnalysisTimeoutMS != 10000 {
		t.Fatalf("corrected configuration = %+v, %v", cfg, err)
	}
}

func TestInvalidConfigurationDoesNotParse(t *testing.T) {
	for name, raw := range map[string]string{
		"missing": "", "version": "version: 2\nvalidation_timeout_ms: 30\nanalysis_timeout_ms: 5\n",
		"zero":     "version: 1\nvalidation_timeout_ms: 0\nanalysis_timeout_ms: 5\n",
		"negative": "version: 1\nvalidation_timeout_ms: 30\nanalysis_timeout_ms: -1\n",
		"overflow": "version: 1\nvalidation_timeout_ms: 9223372036854775807\nanalysis_timeout_ms: 5\n",
		"unknown":  "version: 1\nvalidation_timeout_ms: 30\nanalysis_timeout_ms: 5\ntimeout: 10\n",
	} {
		t.Run(name, func(t *testing.T) {
			configtest.Only(t, map[config.Rel]string{config.SourceParsing: raw})
			tree, err := Parse(context.Background(), nil, []byte("source"), Validation)
			var failure *Failure
			if tree != nil || !errors.As(err, &failure) || failure.Reason != "configuration" {
				t.Fatalf("invalid configuration = %v, %v", tree, err)
			}
		})
	}
}
