package bgprocess

import "testing"

func TestRingBufferReadPageAdvancesOnlyReturnedBytes(t *testing.T) {
	buf := NewRingBuffer(1024)
	buf.Append("stdout", []byte("012345"))
	buf.Append("stdout", []byte("世界"))

	first, next, available, evicted := buf.ReadPage(0, 7)
	if first != "012345" || next != 6 || available != 12 || evicted != 0 {
		t.Fatalf("first page = %q next=%d available=%d evicted=%d", first, next, available, evicted)
	}
	second, next, available, evicted := buf.ReadPage(next, 7)
	if second != "世界" || next != 12 || available != 12 || evicted != 0 {
		t.Fatalf("second page = %q next=%d available=%d evicted=%d", second, next, available, evicted)
	}
}

func TestRingBufferReadPageReportsExactEvictionGap(t *testing.T) {
	buf := NewRingBuffer(5)
	buf.Append("stdout", []byte("abc"))
	buf.Append("stdout", []byte("def"))
	// This append releases the pending eviction.
	buf.Append("stdout", []byte("g"))

	text, next, available, evicted := buf.ReadPage(0, 5)
	if text != "defg" || next != 7 || available != 7 || evicted != 3 {
		t.Fatalf("page = %q next=%d available=%d evicted=%d", text, next, available, evicted)
	}
}
