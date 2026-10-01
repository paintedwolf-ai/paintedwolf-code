// Package sourceview projects executable scripts with original source coordinates.
package sourceview

import "context"

type ScriptProjection struct {
	Source    []byte
	Extension string
	Map       *SourceMap
}

// Scripts returns independently supported regions alongside localized limitations.
// A fatal error means executable block boundaries could not be established.
func Scripts(ctx context.Context, source []byte, component bool) ([]ScriptProjection, []Limitation, error) {
	if component {
		return vueScripts(ctx, source)
	}
	scripts, limitations, err := executableHTMLScripts(source)
	if err != nil {
		return nil, nil, err
	}
	originalLines := lineOffsets(source)
	classic := -1
	var builders []scriptBuilder
	for _, script := range scripts {
		if script.start == script.end {
			continue
		}
		current := classic
		isolated := script.info.mode == moduleScript
		if isolated || classic < 0 {
			current = len(builders)
			builders = append(builders, scriptBuilder{originalLines: originalLines, original: source, extension: script.info.extension})
			if !isolated {
				classic = current
			}
		}
		builders[current].fragments = append(builders[current].fragments, scriptFragment{start: script.start, end: script.end})
	}
	projections := make([]ScriptProjection, 0, len(builders))
	for _, builder := range builders {
		projections = append(projections, builder.build())
	}
	return projections, limitations, nil
}
