package toolcontract

// RightmostNonzeroStatus reduces ordinary stage statuses after the executor has
// classified expected consumer closure. It never derives stream completeness,
// publication eligibility, verification evidence or host stop facts.
// Legacy executor sentinel statuses remain intact; typed operation callers
// validate their ordinary 0..255 statuses before this reduction.
func RightmostNonzeroStatus(statuses []int) int {
	result := 0
	for _, status := range statuses {
		if status != 0 {
			result = status
		}
	}
	return result
}
