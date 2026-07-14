package compose

import "sort"

// namespaceServicePrefix is the compose-spec value prefix ("service:<name>")
// used by network_mode, ipc, and pid to borrow another service's namespace.
// Unlike depends_on (start-order only), this is resolved to a specific
// container id at container-CREATION time: if the referenced service is
// later recreated, the borrower is left pointing at a container that no
// longer exists.
const namespaceServicePrefix = "service:"

// NamespaceDependents returns the names of services (excluding target
// itself) whose network_mode, ipc, or pid field points at target via
// "service:<target>". compose's own up-to-date check only diffs a service's
// OWN declared config, so a dependent is never recreated on its own when the
// service it borrows a namespace from is replaced underneath it, so callers
// must force it explicitly (see ForceRecreateSpec).
func NamespaceDependents(services []Service, target string) []string {
	want := namespaceServicePrefix + target
	seen := make(map[string]bool)
	var out []string
	for _, s := range services {
		if s.Name == target {
			continue
		}
		if s.NetworkMode == want || s.Ipc == want || s.Pid == want {
			if !seen[s.Name] {
				seen[s.Name] = true
				out = append(out, s.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}
