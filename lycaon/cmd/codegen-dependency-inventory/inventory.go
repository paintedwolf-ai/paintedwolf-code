package main

import (
	"errors"
	"fmt"
	"strconv"
)

type section struct {
	cfg  *sectionConfig
	rows []row
}

// collect resolves every section's rows from the tree and applies judgement.
func collect(repo string, p *policy) ([]section, error) {
	reader := newPinReader(repo)
	var sections []section
	var errs []error
	for i := range p.sections {
		sc := &p.sections[i]
		s := section{cfg: sc}
		if sc.Manifest != nil {
			rows, err := manifestRows(reader, sc.Manifest)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", sc.file, err))
				continue
			}
			errs = append(errs, annotate(sc, rows))
			s.rows = rows
		}
		declared := append([]rowConfig(nil), sc.Rows...)
		for _, group := range sc.Groups {
			declared = append(declared, group.Rows...)
		}
		for _, rc := range declared {
			rw, err := declaredRow(reader, rc)
			errs = append(errs, err)
			s.rows = append(s.rows, rw)
		}
		sections = append(sections, s)
	}
	return sections, errors.Join(errs...)
}

// annotate attaches each package judgement to its discovered row and rejects
// judgement for packages the manifest no longer lists.
func annotate(sc *sectionConfig, rows []row) error {
	judged := map[string]judgement{}
	for name, j := range sc.Packages {
		judged[name] = j
	}
	for _, group := range sc.Groups {
		for name, j := range group.Packages {
			judged[name] = j
		}
	}
	var errs []error
	for i := range rows {
		p, ok := judged[rows[i].key]
		if !ok {
			continue
		}
		delete(judged, rows[i].key)
		rows[i].judgement = p
		if p.Updates != "" && rows[i].dependency == "" {
			errs = append(errs, fmt.Errorf("%s: package %s: Dependabot does not manage this entry", sc.file, rows[i].key))
		}
	}
	for name := range judged {
		errs = append(errs, fmt.Errorf("%s: package %s is not a direct dependency in %s; remove its entry",
			sc.file, name, sc.Manifest.Path))
	}
	return errors.Join(errs...)
}

func declaredRow(p *pinReader, rc rowConfig) (row, error) {
	rw := row{key: rc.ID, name: rc.Name, judgement: rc.judgement}
	var errs []error
	for _, ref := range rc.Pins {
		text, err := p.resolve(ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("row %s: %w", rc.ID, err))
		}
		rw.pins = append(rw.pins, value{label: ref.Label, text: text})
	}
	for i, ref := range rc.Upstream {
		rw.upstream = append(rw.upstream, upstreamRef{key: "row:" + rc.ID + "/" + strconv.Itoa(i), label: ref.Label, source: ref})
	}
	return rw, errors.Join(errs...)
}

// comparablePin is the pin an upstream value at index i is measured against.
func (r row) comparablePin(i int) string {
	switch {
	case i < len(r.pins):
		return r.pins[i].text
	case len(r.pins) > 0:
		return r.pins[0].text
	}
	return ""
}

// behind lists pinned→upstream pairs where upstream is a newer release.
func (r row) behind(snap snapshot) [][2]string {
	var out [][2]string
	for i, ref := range r.upstream {
		up, pin := snap.Versions[ref.key], r.comparablePin(i)
		if versionLike(up) && versionLike(pin) && compareVersions(up, pin) > 0 {
			out = append(out, [2]string{pin, up})
		}
	}
	return out
}
