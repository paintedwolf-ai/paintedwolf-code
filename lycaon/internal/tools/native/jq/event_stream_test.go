package jq

import (
 "io"
 "strings"
 "testing"
)

type eventReadGuard struct { source io.Reader; read int }
func(r *eventReadGuard)Read(p []byte)(int,error){n,e:=r.source.Read(p);r.read+=n;return n,e}
func TestJSONEventsDoNotReadTheCompleteDocument(t *testing.T){
 input:=&eventReadGuard{source:strings.NewReader("[0,"+strings.Repeat("1,",100000)+"2]")}
 stream:=NewEventStream(input,EventLimits{MaximumDepth:128,MaximumScalarBytes:1<<20})
 event,err:=stream.Decode()
 if err!=nil{t.Fatalf("first event: %v",err)}
 raw,err:=EncodeJSON(event,false)
 if err!=nil{t.Fatalf("encode event: %v",err)}
 if string(raw)!="[[0],0]"{t.Fatalf("first event: %s",raw)}
 if input.read>4096{t.Fatalf("first event read %d bytes",input.read)}
}
