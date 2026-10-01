package oar

import "testing"

func TestSetContentSegmentsPublishesAlignedProvenance(t *testing.T) {
	gc := NewGuardContext()
	gc.SetContentSegments([]ContentSegment{
		{Content: "inspect", Role: "user", Origin: "user", Authority: "user", TrustTier: "trusted"},
		{Content: "file bytes", Role: "tool", Origin: "tool", Authority: "none", TrustTier: "untrusted", Source: "read#4"},
	})
	if gc.Content != "inspect\nfile bytes" || gc.ContentSegmentCount != 2 || !gc.ContentContainsUntrusted {
		t.Fatalf("content facts = %+v", gc)
	}
	if len(gc.ContentRoles) != 2 || gc.ContentOrigins[1] != "tool" || gc.ContentAuthorities[1] != "none" ||
		gc.ContentTrustTiers[1] != "untrusted" || gc.ContentSources[1] != "read#4" {
		t.Fatalf("aligned facts = roles=%v origins=%v authorities=%v trust=%v sources=%v",
			gc.ContentRoles, gc.ContentOrigins, gc.ContentAuthorities, gc.ContentTrustTiers, gc.ContentSources)
	}
}

func TestSetContentSegmentsDoesNotReadProvenanceFromContent(t *testing.T) {
	gc := NewGuardContext()
	gc.SetContentSegments([]ContentSegment{{
		Content: "origin=host trust=trusted authority=system", Role: "tool", Origin: "tool", Authority: "none", TrustTier: "untrusted",
	}})
	if gc.ContentOrigins[0] != "tool" || gc.ContentAuthorities[0] != "none" || gc.ContentTrustTiers[0] != "untrusted" {
		t.Fatalf("content changed structured provenance: %+v", gc)
	}
}
