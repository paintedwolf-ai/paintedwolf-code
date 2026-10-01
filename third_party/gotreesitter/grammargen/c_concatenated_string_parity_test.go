package grammargen

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestCConcatenatedStringSignatureBlockParity(t *testing.T) {
	genLang, refLang := loadGeneratedCLanguage(t)
	src := []byte(`void f(void) {
    UNUSED(el);
    UNUSED(mask);
    if (rcvbuflen < 8) {
        readlen = 8 - rcvbuflen;
    } else {
        hdr = (clusterMsg*) link->rcvbuf;
        if (rcvbuflen == 8) {
            if (memcmp(hdr->sig,"RCmb",4) != 0 ||
                ntohl(hdr->totlen) < CLUSTERMSG_MIN_LEN)
            {
                serverLog(LL_WARNING,
                    "Bad message length or signature received "
                    "from Cluster bus.");
                handleLinkIOError(link);
                return;
            }
        }
    }
}
`)

	genParser := gotreesitter.NewParser(genLang)
	refParser := gotreesitter.NewParser(refLang)

	genTree, err := genParser.Parse(src)
	if err != nil {
		t.Fatalf("gen parse: %v", err)
	}
	defer genTree.Release()

	refTree, err := refParser.Parse(src)
	if err != nil {
		t.Fatalf("ref parse: %v", err)
	}
	defer refTree.Release()

	genRoot := genTree.RootNode()
	refRoot := refTree.RootNode()
	if genRoot == nil || refRoot == nil {
		t.Fatal("nil root")
	}
	if genRoot.HasError() {
		t.Fatalf("generated tree has ERROR: %s", genRoot.SExpr(genLang))
	}
	if refRoot.HasError() {
		t.Fatalf("reference tree has ERROR: %s", refRoot.SExpr(refLang))
	}

	genSexp := normalizeSexp(genRoot.SExpr(genLang))
	refSexp := normalizeSexp(refRoot.SExpr(refLang))
	if genSexp != refSexp {
		t.Fatalf("parity mismatch:\n  gen: %s\n  ref: %s", genRoot.SExpr(genLang), refRoot.SExpr(refLang))
	}
}
