package resolver

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const (
	resyncPeriod = 5 * time.Minute

	indexPodIP        = "podIP"
	indexClusterIP    = "clusterIP"
	indexEndpointAddr = "endpointAddress"
	indexNodeIP       = "nodeIP"
)

// InformerCaches implements Caches on top of client-go shared informers.
//
// Shared informers rather than raw Watch loops: a Watch loop per resource exhausts file
// descriptors and has to reimplement reconnect and resync. The reference prototype learned this
// the hard way and left a comment about it (ADR-002 §2).
type InformerCaches struct {
	factory informers.SharedInformerFactory

	pods           cache.Indexer
	services       cache.Indexer
	endpointSlices cache.Indexer
	nodes          cache.Indexer
	replicaSets    cache.Indexer
}

// NewInformerCaches builds the informer set. Nothing is watched until Start is called.
//
// The Pod informer is cluster-wide, not node-scoped. Destination resolution routinely looks up
// pods on other nodes — a pod on node A connecting to a pod on node B — so cluster-wide
// visibility is required for correctness. Running an additional node-scoped informer purely for
// source lookups would double pod memory for no behavioural gain.
func NewInformerCaches(client kubernetes.Interface) (*InformerCaches, error) {
	factory := informers.NewSharedInformerFactory(client, resyncPeriod)

	c := &InformerCaches{factory: factory}

	podInformer := factory.Core().V1().Pods().Informer()
	if err := podInformer.AddIndexers(cache.Indexers{indexPodIP: podIPIndex}); err != nil {
		return nil, fmt.Errorf("add pod IP index: %w", err)
	}
	c.pods = podInformer.GetIndexer()

	svcInformer := factory.Core().V1().Services().Informer()
	if err := svcInformer.AddIndexers(cache.Indexers{indexClusterIP: clusterIPIndex}); err != nil {
		return nil, fmt.Errorf("add service ClusterIP index: %w", err)
	}
	c.services = svcInformer.GetIndexer()

	epInformer := factory.Discovery().V1().EndpointSlices().Informer()
	if err := epInformer.AddIndexers(cache.Indexers{indexEndpointAddr: endpointAddressIndex}); err != nil {
		return nil, fmt.Errorf("add endpoint address index: %w", err)
	}
	c.endpointSlices = epInformer.GetIndexer()

	nodeInformer := factory.Core().V1().Nodes().Informer()
	if err := nodeInformer.AddIndexers(cache.Indexers{indexNodeIP: nodeIPIndex}); err != nil {
		return nil, fmt.Errorf("add node IP index: %w", err)
	}
	c.nodes = nodeInformer.GetIndexer()

	c.replicaSets = factory.Apps().V1().ReplicaSets().Informer().GetIndexer()

	// Started for completeness of the ownership chain and namespace metadata even though they
	// are not directly indexed: pods owned by these kinds resolve through OwnerReferences,
	// which carry the name without needing a lookup.
	factory.Apps().V1().Deployments().Informer()
	factory.Apps().V1().StatefulSets().Informer()
	factory.Apps().V1().DaemonSets().Informer()
	factory.Batch().V1().Jobs().Informer()
	factory.Core().V1().Namespaces().Informer()

	return c, nil
}

// Start begins watching and blocks until every cache has synced.
//
// Syncing before the BPF program is attached avoids a burst of unresolved events at startup,
// which would otherwise show up as a spike in the unresolved counter every time the agent
// restarts.
func (c *InformerCaches) Start(ctx context.Context) error {
	c.factory.Start(ctx.Done())

	synced := c.factory.WaitForCacheSync(ctx.Done())
	for informerType, ok := range synced {
		if !ok {
			return fmt.Errorf("informer cache for %v failed to sync", informerType)
		}
	}
	return nil
}

// ── Index functions ─────────────────────────────────────────────────────────────────────────

func podIPIndex(obj any) ([]string, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod.Status.PodIP == "" {
		return nil, nil
	}
	// hostNetwork pods share the node's IP. Indexing them anyway is correct: a connection to
	// that address really is being served by that pod.
	ips := make([]string, 0, len(pod.Status.PodIPs)+1)
	ips = append(ips, pod.Status.PodIP)
	for _, ip := range pod.Status.PodIPs {
		if ip.IP != "" && ip.IP != pod.Status.PodIP {
			ips = append(ips, ip.IP)
		}
	}
	return ips, nil
}

func clusterIPIndex(obj any) ([]string, error) {
	svc, ok := obj.(*corev1.Service)
	if !ok {
		return nil, nil
	}
	var ips []string
	for _, ip := range svc.Spec.ClusterIPs {
		// Headless Services have ClusterIP "None"; they have no address to match.
		if ip != "" && ip != corev1.ClusterIPNone {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 && svc.Spec.ClusterIP != "" && svc.Spec.ClusterIP != corev1.ClusterIPNone {
		ips = append(ips, svc.Spec.ClusterIP)
	}
	return ips, nil
}

func endpointAddressIndex(obj any) ([]string, error) {
	slice, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil, nil
	}
	var addrs []string
	for _, endpoint := range slice.Endpoints {
		// Only endpoints that are actually serving should attribute traffic to a Service.
		if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
			continue
		}
		addrs = append(addrs, endpoint.Addresses...)
	}
	return addrs, nil
}

func nodeIPIndex(obj any) ([]string, error) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil, nil
	}
	var ips []string
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP || addr.Type == corev1.NodeExternalIP {
			ips = append(ips, addr.Address)
		}
	}
	return ips, nil
}

// ── Caches implementation ───────────────────────────────────────────────────────────────────

func (c *InformerCaches) PodByIP(ip netip.Addr) (namespace, name string, owner OwnerRef, ok bool) {
	objs, err := c.pods.ByIndex(indexPodIP, ip.String())
	if err != nil || len(objs) == 0 {
		return "", "", OwnerRef{}, false
	}
	pod, isPod := objs[0].(*corev1.Pod)
	if !isPod {
		return "", "", OwnerRef{}, false
	}
	return pod.Namespace, pod.Name, controllerOf(pod.OwnerReferences), true
}

func (c *InformerCaches) ServiceByClusterIP(ip netip.Addr) (namespace, name string, ok bool) {
	objs, err := c.services.ByIndex(indexClusterIP, ip.String())
	if err != nil || len(objs) == 0 {
		return "", "", false
	}
	svc, isSvc := objs[0].(*corev1.Service)
	if !isSvc {
		return "", "", false
	}
	return svc.Namespace, svc.Name, true
}

// ServicesForEndpoint matches on the EndpointSlice's port, which is the pod-side target port —
// exactly what is observed when a client connects straight to a pod IP. Traffic through a
// ClusterIP carries the Service port instead and is handled earlier in the ladder.
func (c *InformerCaches) ServicesForEndpoint(ip netip.Addr, port uint16) []ServiceRef {
	objs, err := c.endpointSlices.ByIndex(indexEndpointAddr, ip.String())
	if err != nil {
		return nil
	}

	seen := make(map[ServiceRef]struct{}, len(objs))
	var matches []ServiceRef

	for _, obj := range objs {
		slice, isSlice := obj.(*discoveryv1.EndpointSlice)
		if !isSlice {
			continue
		}
		serviceName := slice.Labels[discoveryv1.LabelServiceName]
		if serviceName == "" {
			continue
		}
		if !sliceHasPort(slice, port) {
			continue
		}
		ref := ServiceRef{Namespace: slice.Namespace, Name: serviceName}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		matches = append(matches, ref)
	}
	return matches
}

func sliceHasPort(slice *discoveryv1.EndpointSlice, port uint16) bool {
	// An empty Ports list means "all ports" per the EndpointSlice API.
	if len(slice.Ports) == 0 {
		return true
	}
	for _, p := range slice.Ports {
		if p.Port == nil || *p.Port == int32(port) {
			return true
		}
	}
	return false
}

func (c *InformerCaches) ResolveOwner(namespace string, owner OwnerRef) (OwnerRef, bool) {
	if owner.Kind != "ReplicaSet" {
		return OwnerRef{}, false
	}
	obj, exists, err := c.replicaSets.GetByKey(namespace + "/" + owner.Name)
	if err != nil || !exists {
		return OwnerRef{}, false
	}
	rs, ok := obj.(metav1.Object)
	if !ok {
		return OwnerRef{}, false
	}
	next := controllerOf(rs.GetOwnerReferences())
	if next.Kind == "" {
		return OwnerRef{}, false
	}
	return next, true
}

func (c *InformerCaches) IsNodeIP(ip netip.Addr) bool {
	objs, err := c.nodes.ByIndex(indexNodeIP, ip.String())
	return err == nil && len(objs) > 0
}

// controllerOf returns the controlling owner reference, ignoring non-controller owners.
func controllerOf(refs []metav1.OwnerReference) OwnerRef {
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return OwnerRef{Kind: ref.Kind, Name: ref.Name}
		}
	}
	return OwnerRef{}
}
