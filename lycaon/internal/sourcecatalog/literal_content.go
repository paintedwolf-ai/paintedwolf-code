package sourcecatalog

import (
	"io"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// literalContent derives byte and decoded filters from one bounded stream.
type literalContent struct {
	raw     *literalBloomStream
	size    int64
	decoded *literalBloomStream
	prefix  []byte
	decoder *transform.Writer
	ready   bool
}

func newLiteralContent(size int64) *literalContent {
	return &literalContent{raw: newLiteralBloomStream(size), size: size}
}

func (c *literalContent) Write(raw []byte) (int, error) {
	n := len(raw)
	_, _ = c.raw.Write(raw)
	if !c.ready {
		take := min(3-len(c.prefix), len(raw))
		c.prefix = append(c.prefix, raw[:take]...)
		raw = raw[take:]
		if len(c.prefix) < 3 {
			return n, nil
		}
		if err := c.initialize(); err != nil {
			return n, err
		}
	}
	if c.decoder != nil {
		_, err := c.decoder.Write(raw)
		return n, err
	}
	if c.decoded != nil {
		_, err := c.decoded.Write(raw)
		return n, err
	}
	return n, nil
}

func (c *literalContent) initialize() error {
	c.ready = true
	prefix := c.prefix
	c.prefix = nil
	var decoder transform.Transformer
	if len(prefix) >= 2 && prefix[0] == 0xff && prefix[1] == 0xfe {
		decoder = unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder()
	}
	if len(prefix) >= 2 && prefix[0] == 0xfe && prefix[1] == 0xff {
		decoder = unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM).NewDecoder()
	}
	if decoder != nil {
		c.decoded = newLiteralBloomStream(c.size)
		c.decoder = transform.NewWriter(c.decoded, decoder)
		_, err := c.decoder.Write(prefix)
		return err
	}
	if len(prefix) >= 3 && prefix[0] == 0xef && prefix[1] == 0xbb && prefix[2] == 0xbf {
		c.decoded = newLiteralBloomStream(c.size)
	}
	return nil
}

func (c *literalContent) finish() (literalFileBlooms, error) {
	if !c.ready {
		if err := c.initialize(); err != nil {
			return literalFileBlooms{}, err
		}
	}
	if c.decoder != nil {
		if err := c.decoder.Close(); err != nil {
			return literalFileBlooms{}, err
		}
	}
	bloom, searchable := c.raw.finish()
	facts := literalFileBlooms{raw: literalProjection{bloom: bloom, searchable: searchable}}
	facts.decoded = facts.raw
	facts.shared = c.decoded == nil
	if c.decoded != nil {
		facts.decoded.bloom, facts.decoded.searchable = c.decoded.finish()
	}
	return facts, nil
}

var _ io.Writer = (*literalContent)(nil)
