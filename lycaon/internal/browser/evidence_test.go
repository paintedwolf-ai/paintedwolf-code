package browser

import (
	"fmt"
	"testing"

	"github.com/go-rod/rod/lib/proto"
)

func TestReportNetworkKeepsFailuresBeforeRecentSuccesses(t *testing.T) {
	var records []NetworkRecord
	for i := range MaxReportedNetwork + 40 {
		r := NetworkRecord{Method: "GET", URL: fmt.Sprintf("/asset/%d", i), Status: 200, wallMS: float64(i)}
		if i == 3 {
			r.Status = 500
		}
		if i == 7 {
			r.Status, r.Failure = 0, "net::ERR_CONNECTION_REFUSED"
		}
		records = append(records, r)
	}
	report, omitted := reportNetwork(records, 0)
	if len(report) != MaxReportedNetwork || omitted != 40 {
		t.Fatalf("reported %d, omitted %d", len(report), omitted)
	}
	if report[0].URL != "/asset/3" || report[1].URL != "/asset/7" {
		t.Fatalf("failures were not kept in request order: first %q, %q", report[0].URL, report[1].URL)
	}
	if last := report[len(report)-1]; last.URL != fmt.Sprintf("/asset/%d", MaxReportedNetwork+39) || last.AtMS != float64(MaxReportedNetwork+39) {
		t.Fatalf("the newest request was dropped: %+v", last)
	}
}

func TestEvidenceAttributesAnsweredRequestsInEitherEventOrder(t *testing.T) {
	route := 2
	for _, servedFirst := range []bool{true, false} {
		ev := &pageEvidence{openedWallMS: 999_900, byRequest: map[proto.NetworkRequestID]int{}, served: map[proto.NetworkRequestID]servedBy{}}
		sent := &proto.NetworkRequestWillBeSent{
			RequestID: "r1", Type: proto.NetworkResourceTypeFetch,
			Request: &proto.NetworkRequest{Method: "GET", URL: "http://127.0.0.1/api"}, Timestamp: 10, WallTime: 1000,
		}
		if servedFirst {
			ev.noteServed("r1", networkServedByRoute, &route)
			ev.requestSent(sent)
		} else {
			ev.requestSent(sent)
			ev.noteServed("r1", networkServedByRoute, &route)
		}
		ev.responseReceived(&proto.NetworkResponseReceived{RequestID: "r1", Response: &proto.NetworkResponse{Status: 201}})
		ev.loadingFinished(&proto.NetworkLoadingFinished{RequestID: "r1", Timestamp: 10.25, EncodedDataLength: 42})
		got := ev.since(evidenceMark{}).Network
		if len(got) != 1 {
			t.Fatalf("served first=%v: records %+v", servedFirst, got)
		}
		r := got[0]
		if r.ServedBy != networkServedByRoute || r.Route == nil || *r.Route != 2 || r.Status != 201 || r.Pending || r.DurationMS != 250 || r.Type != "fetch" || r.AtMS != 100 {
			t.Fatalf("served first=%v: record %+v", servedFirst, r)
		}
	}
}

func TestEvidenceRecordsARedirectAsItsOwnHop(t *testing.T) {
	ev := &pageEvidence{byRequest: map[proto.NetworkRequestID]int{}, served: map[proto.NetworkRequestID]servedBy{}}
	ev.requestSent(&proto.NetworkRequestWillBeSent{RequestID: "r", Request: &proto.NetworkRequest{Method: "GET", URL: "http://x/old"}, Timestamp: 1, WallTime: 1})
	ev.requestSent(&proto.NetworkRequestWillBeSent{
		RequestID: "r", Request: &proto.NetworkRequest{Method: "GET", URL: "http://x/new"}, Timestamp: 1.1, WallTime: 1.1,
		RedirectResponse: &proto.NetworkResponse{Status: 302},
	})
	ev.loadingFailed(&proto.NetworkLoadingFailed{RequestID: "r", Timestamp: 1.3, ErrorText: "net::ERR_CONNECTION_REFUSED"})
	got := ev.since(evidenceMark{}).Network
	if len(got) != 2 || got[0].Status != 302 || got[0].Pending || got[1].URL != "http://x/new" || got[1].Failure != "net::ERR_CONNECTION_REFUSED" {
		t.Fatalf("redirect records = %+v", got)
	}
}
