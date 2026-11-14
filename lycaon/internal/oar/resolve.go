package oar

// resolve picks the decision: a block, else a nudge, else a warn, and within
// the winning effect the first rule in evaluation order ([OAR-EVAL-4],
// [OAR-EVAL-10]). The others are only on the applied-rules trace.
func resolve(blocks, nudges, warns []Decision) *Decision {
	for _, group := range [][]Decision{blocks, nudges, warns} {
		if len(group) > 0 {
			d := group[0]
			return &d
		}
	}
	return nil
}
