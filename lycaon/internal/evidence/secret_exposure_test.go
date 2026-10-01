package evidence

import "testing"

func TestRecordMarksSecretExposure(t *testing.T) {
	if !RecordMarksSecretExposure(Record{Handle: InheritSecretExposureHandle}) {
		t.Fatal("inherit marker must mark")
	}
	if !RecordMarksSecretExposure(Record{Handle: SecretExposureHandlePrefix + "deadbeef"}) {
		t.Fatal("path marker must mark")
	}
	if !RecordMarksSecretExposure(Record{Handle: "root:child:" + InheritSecretExposureHandle}) {
		t.Fatal("namespaced inherit marker must mark")
	}
	if !RecordMarksSecretExposure(Record{Handle: "root:child:" + SecretExposureHandlePrefix + "deadbeef"}) {
		t.Fatal("namespaced path marker must mark")
	}
	if RecordMarksSecretExposure(Record{Handle: "read#1", SourceTool: "read", Path: ".env"}) {
		t.Fatal("ordinary read must not mark — only secret_exposure# handles")
	}
	if RecordMarksSecretExposure(Record{Handle: InheritUntrustedHandle}) {
		t.Fatal("untrusted inherit must not mark secret exposure")
	}
}
