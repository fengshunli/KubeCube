/*
Copyright 2021 KubeCube Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cluster

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "github.com/kubecube-io/kubecube/pkg/apis/cluster/v1"
	v1 "github.com/kubecube-io/kubecube/pkg/apis/quota/v1"
	"github.com/kubecube-io/kubecube/pkg/clients"
	"github.com/kubecube-io/kubecube/pkg/clog"
	"github.com/kubecube-io/kubecube/pkg/multicluster"
	mgrclient "github.com/kubecube-io/kubecube/pkg/multicluster/client"
	"github.com/kubecube-io/kubecube/pkg/quota"
	"github.com/kubecube-io/kubecube/pkg/utils/constants"
	"github.com/kubecube-io/kubecube/pkg/utils/strproc"
)

type clusterInfoOpts struct {
	statusFilter      string
	nodeLabelSelector labels.Selector
	pruneInfo         bool
}

// makeClusterInfos make cluster info with clusters given
func makeClusterInfos(ctx context.Context, clusters clusterv1.ClusterList, pivotCli mgrclient.Client, opts clusterInfoOpts) ([]clusterInfo, error) {
	// populate cluster info one by one
	infos := make([]clusterInfo, 0)
	for _, item := range clusters.Items {
		info := clusterInfo{}
		clusterName := item.Name

		// populate metadata of cluster
		metadataInfo, err := makeMetadataInfo(ctx, pivotCli, clusterName, opts)
		if err != nil {
			clog.Warn(err.Error())
			continue // ignore query error and continue now
		}
		info.clusterMetaInfo = metadataInfo

		// set cluster status and do not populate livedata if abnormal
		internalCluster, err := multicluster.Interface().Get(clusterName)
		if internalCluster != nil && err != nil {
			metadataInfo.Status = string(clusterv1.ClusterAbnormal)
		}
		if internalCluster == nil || metadataInfo.Status == string(clusterv1.ClusterAbnormal) {
			if len(opts.statusFilter) == 0 {
				infos = append(infos, clusterInfo{clusterMetaInfo: metadataInfo})
			} else if metadataInfo.Status == opts.statusFilter {
				infos = append(infos, clusterInfo{clusterMetaInfo: metadataInfo})
			}
			continue
		}

		// populate livedata of cluster
		if !opts.pruneInfo {
			cli := internalCluster.Client
			livedataInfo, err := makeLivedataInfo(ctx, cli, clusterName, opts)
			if err != nil {
				return nil, err
			}
			info.clusterLivedataInfo = livedataInfo
		}

		// filter by query status param
		if len(opts.statusFilter) == 0 {
			infos = append(infos, info)
		} else if info.Status == opts.statusFilter {
			infos = append(infos, info)
		}
	}

	return infos, nil
}

func podRequestsAndLimits(pod *corev1.Pod) (reqs, limits corev1.ResourceList) {
	reqs, limits = corev1.ResourceList{}, corev1.ResourceList{}
	for _, container := range pod.Spec.Containers {
		addResourceList(reqs, container.Resources.Requests)
		addResourceList(limits, container.Resources.Limits)
	}
	// init containers define the minimum of any resource
	for _, container := range pod.Spec.InitContainers {
		maxResourceList(reqs, container.Resources.Requests)
		maxResourceList(limits, container.Resources.Limits)
	}

	// Add overhead for running a pod to the sum of requests and to non-zero limits:
	if pod.Spec.Overhead != nil {
		addResourceList(reqs, pod.Spec.Overhead)

		for name, quantity := range pod.Spec.Overhead {
			if value, ok := limits[name]; ok && !value.IsZero() {
				value.Add(quantity)
				limits[name] = value
			}
		}
	}
	return
}

// addResourceList adds the resources in newList to list
func addResourceList(list, new corev1.ResourceList) {
	for name, quantity := range new {
		if value, ok := list[name]; !ok {
			list[name] = quantity.DeepCopy()
		} else {
			value.Add(quantity)
			list[name] = value
		}
	}
}

// maxResourceList sets list to the greater of list/newList for every resource
// either list
func maxResourceList(list, new corev1.ResourceList) {
	for name, quantity := range new {
		if value, ok := list[name]; !ok {
			list[name] = quantity.DeepCopy()
			continue
		} else {
			if quantity.Cmp(value) > 0 {
				list[name] = quantity.DeepCopy()
			}
		}
	}
}

func makeMonitorInfo(ctx context.Context, cluster string) (*monitorInfo, error) {
	cli := clients.Interface().Kubernetes(cluster)
	if cli == nil {
		return nil, fmt.Errorf("cluster %v abnormal", cluster)
	}

	nodesMC, err := cli.Metrics().MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("get cluster %v nodes metrics failed: %v", cluster, err)
	}

	info := &monitorInfo{}
	for _, m := range nodesMC.Items {
		info.UsedCPU += strproc.Str2int(m.Usage.Cpu().String())/1000000 + 1
		info.UsedMem += strproc.Str2int(m.Usage.Memory().String()) / 1024
		info.UsedStorage += (strproc.Str2int(m.Usage.Storage().String()) + 1) / 1024
		info.UsedStorageEphemeral += (strproc.Str2int(m.Usage.StorageEphemeral().String()) + 1) / 1024
	}

	nodes := corev1.NodeList{}
	err = cli.Cache().List(ctx, &nodes)
	if err != nil {
		return nil, fmt.Errorf("get cluster %v nodes failed: %v", cluster, err)
	}

	info.NodeCount = len(nodes.Items)

	for _, n := range nodes.Items {
		info.TotalCPU += strproc.Str2int(n.Status.Capacity.Cpu().String()) * 1000
		info.TotalMem += strproc.Str2int(n.Status.Capacity.Memory().String()) / 1024
		info.TotalStorage += (strproc.Str2int(n.Status.Capacity.Storage().String()) + 1) / 1024
		info.TotalStorageEphemeral += (strproc.Str2int(n.Status.Capacity.StorageEphemeral().String()) + 1) / 1024
	}

	ns := corev1.NamespaceList{}
	err = cli.Cache().List(ctx, &ns)
	if err != nil {
		return nil, fmt.Errorf("get cluster %v namespace failed: %v", cluster, err)
	}

	info.NamespaceCount = len(ns.Items)

	return info, nil
}

// makeMetadataInfo just get metadata of cluster.
func makeMetadataInfo(ctx context.Context, pivotCli mgrclient.Client, clusterName string, opts clusterInfoOpts) (clusterMetaInfo, error) {
	info := clusterMetaInfo{}

	cluster := clusterv1.Cluster{}
	clusterKey := types.NamespacedName{Name: clusterName}
	err := pivotCli.Direct().Get(ctx, clusterKey, &cluster)
	if err != nil {
		return info, fmt.Errorf("get cluster %v failed: %v", clusterName, err)
	}

	state := cluster.Status.State
	if state == nil {
		processState := clusterv1.ClusterProcessing
		state = &processState
	}

	// set up cluster meta info
	info.ClusterName = clusterName
	info.Status = string(*state)
	info.ClusterDescription = cluster.Spec.Description
	info.CreateTime = cluster.CreationTimestamp.Time
	info.IsMemberCluster = cluster.Spec.IsMemberCluster
	info.IsWritable = cluster.Spec.IsWritable
	info.HarborAddr = cluster.Spec.HarborAddr
	info.KubeApiServer = cluster.Spec.KubernetesAPIEndpoint
	info.NetworkType = cluster.Spec.NetworkType
	info.IngressDomainSuffix = cluster.Spec.IngressDomainSuffix
	info.Labels = cluster.Labels
	info.Annotations = cluster.Annotations

	if info.Annotations == nil {
		info.Annotations = make(map[string]string)
	}

	// set cluster cn name same as en name by default
	if _, ok := info.Annotations[constants.CubeCnAnnotation]; !ok {
		info.Annotations[constants.CubeCnAnnotation] = cluster.Name
	}

	return info, nil
}

// makeLivedataInfo populate livedata of cluster, sometimes be slow.
func makeLivedataInfo(ctx context.Context, cli mgrclient.Client, cluster string, opts clusterInfoOpts) (clusterLivedataInfo, error) {
	info := clusterLivedataInfo{}

	// populate nodes used resources info by metrics api
	metricListCtx, cancel := context.WithTimeout(ctx, time.Second)
	nodesMc, err := cli.Metrics().MetricsV1beta1().NodeMetricses().List(metricListCtx, metav1.ListOptions{LabelSelector: opts.nodeLabelSelector.String()})
	if err != nil {
		// record error from metric server, but ensure return normal
		clog.Warn("get cluster %v nodes metrics failed: %v", cluster, err)
	} else {
		for _, m := range nodesMc.Items {
			info.UsedCPU += int(m.Usage.Cpu().MilliValue())                                           // 1000 m
			info.UsedMem += convertUnit(m.Usage.Memory().String(), strproc.Mi)                        // 1024 Mi
			info.UsedStorage += convertUnit(m.Usage.Storage().String(), strproc.Mi)                   // 1024 Mi
			info.UsedStorageEphemeral += convertUnit(m.Usage.StorageEphemeral().String(), strproc.Mi) // 1024 Mi
		}
	}

	// releases resources if call metric api completes before timeout elapses
	// or any errors occurred
	cancel()

	// populate node resources info
	nodes := corev1.NodeList{}
	err = cli.Cache().List(ctx, &nodes, &client.ListOptions{LabelSelector: opts.nodeLabelSelector})
	if err != nil {
		return info, fmt.Errorf("get cluster %v nodes failed: %v", cluster, err)
	}

	info.NodeCount = len(nodes.Items)

	for _, n := range nodes.Items {
		info.TotalCPU += int(n.Status.Capacity.Cpu().MilliValue())                                           // 1000 m
		info.TotalMem += convertUnit(n.Status.Capacity.Memory().String(), strproc.Mi)                        // 1024 Mi
		info.TotalStorage += convertUnit(n.Status.Capacity.Storage().String(), strproc.Mi)                   // 1024 Mi
		info.TotalStorageEphemeral += convertUnit(n.Status.Capacity.StorageEphemeral().String(), strproc.Mi) // 1024 Mi
	}

	ns := corev1.NamespaceList{}
	err = cli.Cache().List(ctx, &ns)
	if err != nil {
		return info, fmt.Errorf("get cluster %v namespace failed: %v", cluster, err)
	}

	info.NamespaceCount = len(ns.Items)

	podList := &corev1.PodList{}
	err = cli.Cache().List(context.TODO(), podList)
	if err != nil {
		return info, err
	}
	nodesName := sets.NewString()
	for i := range nodes.Items {
		nodesName.Insert(nodes.Items[i].Name)
	}

	for i := range podList.Items {
		statusPhase := podList.Items[i].Status.Phase
		if nodesName.Has(podList.Items[i].Spec.NodeName) && statusPhase != corev1.PodSucceeded && statusPhase != corev1.PodFailed {
			req, limit := podRequestsAndLimits(&podList.Items[i])
			cpuReq, cpuLimit, memoryReq, memoryLimit := req[corev1.ResourceCPU], limit[corev1.ResourceCPU], req[corev1.ResourceMemory], limit[corev1.ResourceMemory]
			info.UsedCPURequest += int(cpuReq.MilliValue())                    // 1000 m
			info.UsedCPULimit += int(cpuLimit.MilliValue())                    // 1000 m
			info.UsedMemRequest += convertUnit(memoryReq.String(), strproc.Mi) // 1024 Mi
			info.UsedMemLimit += convertUnit(memoryLimit.String(), strproc.Mi) // 1024 Mi
		}
	}

	return info, nil
}

func convertUnit(data, expectedUnit string) int {
	value, err := strproc.BinaryUnitConvert(data, expectedUnit)
	if err != nil {
		// error should not occur here
		clog.Warn(err.Error())
	}

	// note: decimal point will be truncated.
	return int(value)
}

// isRelateWith return true if third level namespace exist under of ancestor namespace
func isRelateWith(namespace string, cli cache.Cache, depth string, ctx context.Context) (bool, error) {
	if depth == constants.HncCurrentDepth {
		return true, nil
	}

	hncLabel := namespace + constants.HncSuffix
	nsList := corev1.NamespaceList{}

	err := cli.List(ctx, &nsList)
	if err != nil {
		return false, err
	}

	for _, ns := range nsList.Items {
		if d, ok := ns.Labels[hncLabel]; ok {
			if d == depth {
				return true, nil
			}
		}
	}

	return false, nil
}

// listClusterNames list all clusters name
func listClusterNames() []string {
	clusterNames := make([]string, 0)
	clusters := multicluster.Interface().FuzzyCopy()

	for _, c := range clusters {
		clusterNames = append(clusterNames, c.Name)
	}

	return clusterNames
}

// getClustersByNamespace get clusters where the namespace work in
func getClustersByNamespace(namespace string, ctx context.Context) ([]string, error) {
	clusterNames := make([]string, 0)
	clusters := multicluster.Interface().FuzzyCopy()
	key := types.NamespacedName{Name: namespace}

	for _, cluster := range clusters {
		cli := cluster.Client.Cache()
		ns := corev1.Namespace{}
		isRelated := true

		err := cli.Get(ctx, key, &ns)
		if err != nil {
			if errors.IsNotFound(err) {
				clog.Debug("cluster %s not work with namespace %v", cluster.Name, key.Name)
				continue
			}
			clog.Error("get namespace %v from cluster %v failed: %v", key.Name, cluster.Name, err)
			return nil, err
		}

		// if namespace is tenant hnc
		if t, ok := ns.Labels[constants.TenantLabel]; ok {
			isRelated, err = isRelateWith(t, cli, constants.HncTenantDepth, ctx)
			if err != nil {
				clog.Error("judge relationship of cluster % v and namespace %v failed: %v", cluster.Name, key.Name, err)
				return nil, err
			}
		}

		// if namespace is project hnc
		if p, ok := ns.Labels[constants.ProjectLabel]; ok {
			isRelated, err = isRelateWith(p, cli, constants.HncProjectDepth, ctx)
			if err != nil {
				clog.Error("judge relationship of cluster % v and namespace %v failed: %v", cluster.Name, key.Name, err)
				return nil, err
			}
		}

		// add related cluster to result
		if isRelated {
			clusterNames = append(clusterNames, cluster.Name)
		}
	}

	return clusterNames, nil
}

// getClustersByProject get related clusters by given project
func getClustersByProject(ctx context.Context, project string) (*clusterv1.ClusterList, error) {
	var clusterItem []clusterv1.Cluster

	projectLabel := constants.ProjectNsPrefix + project + constants.HncSuffix
	labelSelector, err := labels.Parse(fmt.Sprintf("%v=%v", projectLabel, "1"))
	if err != nil {
		return nil, err
	}

	clusters := multicluster.Interface().FuzzyCopy()
	for _, cluster := range clusters {
		cli := cluster.Client.Cache()
		nsList := corev1.NamespaceList{}
		err := cli.List(ctx, &nsList, &client.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return nil, err
		}
		// this cluster is related with project if we found any namespaces under given project
		if len(nsList.Items) > 0 {
			clusterItem = append(clusterItem, *cluster.RawCluster)
		}
	}

	return &clusterv1.ClusterList{Items: clusterItem}, nil
}

func getAssignedResource(cli mgrclient.Client, cluster string) (cpu resource.Quantity, mem resource.Quantity, gpu resource.Quantity, err error) {
	labelSelector, err := labels.Parse(fmt.Sprintf("%v=%v", constants.ClusterLabel, cluster))
	if err != nil {
		return resource.Quantity{}, resource.Quantity{}, resource.Quantity{}, err
	}

	listObjs := v1.CubeResourceQuotaList{}
	err = cli.Direct().List(context.Background(), &listObjs, &client.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return resource.Quantity{}, resource.Quantity{}, resource.Quantity{}, err
	}

	for _, obj := range listObjs.Items {
		hard := obj.Spec.Hard
		if v, ok := hard[corev1.ResourceRequestsCPU]; ok {
			cpu.Add(v)
		}
		if v, ok := hard[corev1.ResourceRequestsMemory]; ok {
			mem.Add(v)
		}
		if v, ok := hard[quota.ResourceNvidiaGPU]; ok {
			gpu.Add(v)
		}
	}
	return cpu, mem, gpu, err
}
