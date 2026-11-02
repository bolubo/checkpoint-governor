package policy

import "sort"

// Helpers the risk core shares with the rest of the package. The interval policy that used to own
// these is deleted (decision 0014, 0015 §5).

// newestCopy returns the copy with the highest step, or nil when there is none.
func newestCopy(copies []Copy) *Copy {
	var newest *Copy
	for i := range copies {
		if newest == nil || copies[i].Step > newest.Step {
			newest = &copies[i]
		}
	}
	return newest
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// policies holds every policy the command line can run. It ships the risk core only.
var policies = map[string]Policy{
	riskVersion: Risk{},
}

// Lookup returns the policy with the given name.
func Lookup(name string) (Policy, bool) {
	p, ok := policies[name]
	return p, ok
}

// Available lists the names of all known policies, sorted.
func Available() []string {
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
