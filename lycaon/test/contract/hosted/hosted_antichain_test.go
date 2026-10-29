package contract

import (
	"slices"
	"strconv"
	"testing"
)

// Capacitated matching partitions weighted copies of comparable jobs into
// chains. The unmatched weight is the widest set of concurrent jobs.
func widestAntichain(ancestors map[string]map[string]bool, weight func(string) int) int {
	jobs := make([]string, 0, len(ancestors))
	for job := range ancestors {
		jobs = append(jobs, job)
	}
	slices.Sort(jobs)
	source, sink := 2*len(jobs), 2*len(jobs)+1
	capacity := make([][]int, sink+1)
	for i := range capacity {
		capacity[i] = make([]int, sink+1)
	}
	total := 0
	for i, job := range jobs {
		w := weight(job)
		capacity[source][i], capacity[len(jobs)+i][sink] = w, w
		total += w
	}
	for i, job := range jobs {
		for j, other := range jobs {
			if ancestors[job][other] {
				capacity[i][len(jobs)+j] = total
			}
		}
	}
	flow := 0
	for {
		parent := make([]int, len(capacity))
		for i := range parent {
			parent[i] = -1
		}
		parent[source] = source
		queue := []int{source}
		for len(queue) > 0 && parent[sink] < 0 {
			u := queue[0]
			queue = queue[1:]
			for v, available := range capacity[u] {
				if available > 0 && parent[v] < 0 {
					parent[v] = u
					queue = append(queue, v)
				}
			}
		}
		if parent[sink] < 0 {
			return total - flow
		}
		amount := total
		for v := sink; v != source; v = parent[v] {
			amount = min(amount, capacity[parent[v]][v])
		}
		for v := sink; v != source; v = parent[v] {
			capacity[parent[v]][v] -= amount
			capacity[v][parent[v]] += amount
		}
		flow += amount
	}
}

func exhaustiveAntichain(ancestors map[string]map[string]bool, weights []int) int {
	widest := 0
	for subset := 0; subset < 1<<len(weights); subset++ {
		sum, independent := 0, true
		for i, weight := range weights {
			if subset&(1<<i) == 0 {
				continue
			}
			sum += weight
			for j := range weights {
				if subset&(1<<j) != 0 && ancestors[strconv.Itoa(i)][strconv.Itoa(j)] {
					independent = false
				}
			}
		}
		if independent {
			widest = max(widest, sum)
		}
	}
	return widest
}

func TestWeightedAntichainMatchesExhaustiveDAGs(t *testing.T) {
	for count := 0; count <= 5; count++ {
		for edges := 0; edges < 1<<(count*(count-1)/2); edges++ {
			ancestors := map[string]map[string]bool{}
			bit := 0
			for i := 0; i < count; i++ {
				parents := map[string]bool{}
				for j := 0; j < i; j++ {
					if edges&(1<<bit) != 0 {
						parents[strconv.Itoa(j)] = true
						for ancestor := range ancestors[strconv.Itoa(j)] {
							parents[ancestor] = true
						}
					}
					bit++
				}
				ancestors[strconv.Itoa(i)] = parents
			}
			for pattern := 0; pattern < 3; pattern++ {
				weights := make([]int, count)
				for i := range weights {
					weights[i] = 1
					if pattern > 0 {
						weights[i] = (i + pattern) % 4
					}
				}
				weight := func(job string) int {
					i, _ := strconv.Atoi(job)
					return weights[i]
				}
				got, want := widestAntichain(ancestors, weight), exhaustiveAntichain(ancestors, weights)
				if got != want {
					t.Fatalf("nodes=%d edges=%d weights=%v: got %d, want %d", count, edges, weights, got, want)
				}
			}
		}
	}
}

func TestWeightedAntichainBeyondSubsetEnumeration(t *testing.T) {
	for _, chain := range []bool{false, true} {
		ancestors := map[string]map[string]bool{}
		for i := 0; i < 64; i++ {
			parents := map[string]bool{}
			if chain {
				for j := 0; j < i; j++ {
					parents[strconv.Itoa(j)] = true
				}
			}
			ancestors[strconv.Itoa(i)] = parents
		}
		want := 128
		if chain {
			want = 2
		}
		if got := widestAntichain(ancestors, func(string) int { return 2 }); got != want {
			t.Fatalf("chain=%t: got %d, want %d", chain, got, want)
		}
	}
}
