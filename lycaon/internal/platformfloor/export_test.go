package platformfloor

// MachOArches preserves the configured architecture order.
func MachOArches() []string {
	out := make([]string, 0, len(darwinArches))
	for _, goArch := range darwinArches {
		if machO, ok := machOArchForGoArch[goArch]; ok {
			out = append(out, machO)
		}
	}
	return out
}
