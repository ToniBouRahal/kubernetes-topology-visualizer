package query

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
)

// Kinds are the node kinds a user may filter on: the six workload kinds of the contract, plus
// the external node's.
var Kinds = []string{"Service", "Deployment", "StatefulSet", "DaemonSet", "Job", "Pod", "External"}

// CanonicalKind accepts a kind in any letter case and returns its contract spelling.
func CanonicalKind(k string) (string, error) {
	for _, known := range Kinds {
		if strings.EqualFold(k, known) {
			return known, nil
		}
	}
	return "", fmt.Errorf("unknown kind %q: want one of %s", k, strings.Join(Kinds, ", "))
}

// ErrNotObserved is a name with no node in the window. Absent from a window is not proof the
// workload does not exist (limitations §1.4), so the wording never says it does not.
var ErrNotObserved = errors.New("not observed in this window")

// AmbiguousError is a name that matches several nodes; the caller lists them and asks for a
// namespace or kind.
type AmbiguousError struct {
	Name       string
	Candidates []api.GraphNode
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches %d nodes; narrow it with -n NAMESPACE or --kind KIND", e.Name, len(e.Candidates))
}

// Resolve finds the one node called name, optionally narrowed by namespace and kind, among the
// nodes of a graph response. It compares the response's name, namespace and kind fields — never
// a parsed ID — and the returned node's ID is passed on verbatim. kind must already be canonical
// (or empty).
func Resolve(nodes []api.GraphNode, name, namespace, kind string) (api.GraphNode, error) {
	var matches []api.GraphNode
	for _, n := range nodes {
		if n.Name != name {
			continue
		}
		if namespace != "" && (n.Namespace == nil || *n.Namespace != namespace) {
			continue
		}
		if kind != "" && n.Kind != kind {
			continue
		}
		matches = append(matches, n)
	}
	switch len(matches) {
	case 0:
		return api.GraphNode{}, ErrNotObserved
	case 1:
		return matches[0], nil
	default:
		return api.GraphNode{}, &AmbiguousError{Name: name, Candidates: matches}
	}
}
