package docext

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/promptattach/docformat"
)

func extractODF(ctx context.Context, format docformat.Format, req boundedRequest) (Result, error) {
	if _, err := checkZipExpansion(req.Bytes, req.Bounds); err != nil {
		return Result{}, err
	}
	out, err := extractInWorker(ctx, format, req)
	if err != nil {
		return Result{}, err
	}
	return out, nil
}

func extractODFDirect(format docformat.Format, req boundedRequest) (Result, error) {
	zr, err := checkZipExpansion(req.Bytes, req.Bounds)
	if err != nil {
		return Result{}, err
	}
	var content *zip.File
	for _, f := range zr.File {
		if f.Name == "content.xml" || strings.HasSuffix(f.Name, "/content.xml") {
			content = f
			break
		}
	}
	if content == nil {
		return Result{}, attacherr.Unsupported("odf package missing content.xml")
	}
	raw, err := readZipFile(content, req.Bounds.MaxBodyBytes)
	if err != nil {
		return Result{}, attacherr.TooLarge("odf content.xml exceeds bound")
	}

	text, units, err := extractODFXML(raw, format)
	if err != nil {
		return Result{}, attacherr.Unsupported("odf parse failed")
	}
	maxUnits := req.Bounds.MaxPages
	if format == docformat.ODP {
		maxUnits = req.Bounds.MaxSlides
	}
	if units > maxUnits {
		return Result{}, attacherr.TooLarge("document exceeds page/slide cap")
	}
	return Result{Text: strings.TrimSpace(text), UnitCount: units}, nil
}

func extractODFXML(raw []byte, format docformat.Format) (text string, units int, err error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	dec.Entity = map[string]string{}

	var b strings.Builder
	var cell strings.Builder
	var row []string
	inText := false
	inCell := false

	flushRow := func() {
		if len(row) == 0 {
			return
		}
		b.WriteString(strings.Join(row, ","))
		b.WriteByte('\n')
		row = row[:0]
	}

	for {
		tok, e := dec.Token()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return "", 0, e
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "draw-page":
				units++
				if format == docformat.ODP && b.Len() > 0 {
					b.WriteString("\n\n")
				}
			case "table":
				if format == docformat.ODS {
					units++
					if b.Len() > 0 {
						b.WriteString("\n\n")
					}
				}
			case "table-row":
				row = row[:0]
			case "table-cell", "covered-table-cell":
				inCell = true
				cell.Reset()
			case "p", "h", "span", "text":
				inText = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "h":
				inText = false
				if format != docformat.ODS && b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
					b.WriteByte('\n')
				}
			case "span", "text":
				inText = false
			case "table-cell", "covered-table-cell":
				if inCell {
					row = append(row, cell.String())
					inCell = false
				}
			case "table-row":
				flushRow()
			}
		case xml.CharData:
			s := string(t)
			if strings.TrimSpace(s) == "" {
				continue
			}
			if inCell {
				cell.WriteString(s)
				continue
			}
			if inText || format == docformat.ODP {
				b.WriteString(s)
			}
		}
	}
	if units == 0 {
		units = 1
	}
	return b.String(), units, nil
}
