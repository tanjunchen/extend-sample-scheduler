# k8s-scheduler-extender-demo

This is an example of [Kubernetes Scheduler Extender](https://github.com/kubernetes/community/blob/master/contributors/design-proposals/scheduling/scheduler_extender.md)

## extender 自定义 k8s 调度器

kubernetes 的 scheduler 调度器的设计中为用户预留了两种扩展方案 SchdulerExtender 与 Framework，目前推荐使用 Framework。

SchedulerExtender 是 Kubernetes 外部扩展方式，用户可以根据需求独立构建调度服务，实现对应的远程调用接口, 
scheduler 在调度的对应阶段会根据用户定义的资源和接口来进行远程调用，对应的 service 根据自己的资源 和 scheduler 传递过来的中间调度结果来进行决策。

本示例简单演示 SchdulerExtender 拓展。

## 调度算法

Predicates

    存储相关
    Pod 和 Node 匹配相关
    Pod 和 Pod 匹配相关
    Pod 打散相关(EvenPodsSpread CheckServiceAffinity)
    
Priorities

    Node 水位
    Pod 打散
    Node 亲和与反亲和
    Pod 亲和与反亲和
具体详情可以参见 [K8s Scheduler 调度器](https://zhuanlan.zhihu.com/p/101908480)

## 源码介绍

参见[官网](https://github.com/kubernetes/kubernetes)

SchedulerExtender

```
// SchedulerExtender is an interface for external processes to influence scheduling
// decisions made by Kubernetes. This is typically needed for resources not directly
// managed by Kubernetes.
type SchedulerExtender interface {
	// Name returns a unique name that identifies the extender.
	Name() string

	// Filter based on extender-implemented predicate functions. The filtered list is
	// expected to be a subset of the supplied list. failedNodesMap optionally contains
	// the list of failed nodes and failure reasons.
    // 预选阶段
	Filter(pod *v1.Pod, nodes []*v1.Node) (filteredNodes []*v1.Node, failedNodesMap extenderv1.FailedNodesMap, err error)

	// Prioritize based on extender-implemented priority functions. The returned scores & weight
	// are used to compute the weighted score for an extender. The weighted scores are added to
	// the scores computed by Kubernetes scheduler. The total scores are used to do the host selection.
    // 优选阶段
	Prioritize(pod *v1.Pod, nodes []*v1.Node) (hostPriorities *extenderv1.HostPriorityList, weight int64, err error)

	// Bind delegates the action of binding a pod to a node to the extender.
    // extender 对 pod 进行绑定操作
	Bind(binding *v1.Binding) error

	// IsBinder returns whether this extender is configured for the Bind method.
    // 扩展是否支持 bind
	IsBinder() bool

	// IsInterested returns true if at least one extended resource requested by
	// this pod is managed by this extender.
    // 是否对对应的 pod 资源感兴趣
	IsInterested(pod *v1.Pod) bool

	// ProcessPreemption returns nodes with their victim pods processed by extender based on
	// given:
	//   1. Pod to schedule
	//   2. Candidate nodes and victim pods (nodeToVictims) generated by previous scheduling process.
	//   3. nodeNameToInfo to restore v1.Node from node name if extender cache is enabled.
	// The possible changes made by extender may include:
	//   1. Subset of given candidate nodes after preemption phase of extender.
	//   2. A different set of victim pod for every given candidate node after preemption phase of extender.
    // 抢占阶段
	ProcessPreemption(
		pod *v1.Pod,
		nodeToVictims map[*v1.Node]*extenderv1.Victims,
		nodeInfos listers.NodeInfoLister) (map[*v1.Node]*extenderv1.Victims, error)

	// SupportsPreemption returns if the scheduler extender support preemption or not.
    // 是否支持抢占
	SupportsPreemption() bool

	// IsIgnorable returns true indicates scheduling should not fail when this extender
	// is unavailable. This gives scheduler ability to fail fast and tolerate non-critical extenders as well.
	IsIgnorable() bool
}
```

```
默认实现
// HTTPExtender implements the SchedulerExtender interface.
type HTTPExtender struct {
	extenderURL      string
	preemptVerb      string
	filterVerb       string
	prioritizeVerb   string
	bindVerb         string
	weight           int64
	client           *http.Client
	nodeCacheCapable bool
	managedResources sets.String
	ignorable        bool
}
```

```
远程通信接口
// Helper function to send messages to the extender
func (h *HTTPExtender) send(action string, args interface{}, result interface{}) error {
	out, err := json.Marshal(args)
	if err != nil {
		return err
	}

	url := strings.TrimRight(h.extenderURL, "/") + "/" + action

	req, err := http.NewRequest("POST", url, bytes.NewReader(out))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Failed %v with extender at URL %v, code %v", action, url, resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(result)
}
```

```
远程过滤接口
// Filter based on extender implemented predicate functions. The filtered list is
// expected to be a subset of the supplied list; otherwise the function returns an error.
// failedNodesMap optionally contains the list of failed nodes and failure reasons.
func (h *HTTPExtender) Filter(
	pod *v1.Pod,
	nodes []*v1.Node,
) ([]*v1.Node, extenderv1.FailedNodesMap, error) {
	var (
		result     extenderv1.ExtenderFilterResult
		nodeList   *v1.NodeList
		nodeNames  *[]string
		nodeResult []*v1.Node
		args       *extenderv1.ExtenderArgs
	)
	fromNodeName := make(map[string]*v1.Node)
	for _, n := range nodes {
		fromNodeName[n.Name] = n
	}

	if h.filterVerb == "" {
		return nodes, extenderv1.FailedNodesMap{}, nil
	}
    
    // 如果缓存数据，则只需要传递 node 的名称，而不需要传递 node 元数据
	if h.nodeCacheCapable {
		nodeNameSlice := make([]string, 0, len(nodes))
		for _, node := range nodes {
			nodeNameSlice = append(nodeNameSlice, node.Name)
		}
		nodeNames = &nodeNameSlice
	} else {
		nodeList = &v1.NodeList{}
		for _, node := range nodes {
			nodeList.Items = append(nodeList.Items, *node)
		}
	}

	args = &extenderv1.ExtenderArgs{
		Pod:       pod,
		Nodes:     nodeList,
		NodeNames: nodeNames,
	}
    
    // 调用对应 Service 的 filter 接口
	if err := h.send(h.filterVerb, args, &result); err != nil {
		return nil, nil, err
	}
	if result.Error != "" {
		return nil, nil, fmt.Errorf(result.Error)
	}

	if h.nodeCacheCapable && result.NodeNames != nil {
		nodeResult = make([]*v1.Node, len(*result.NodeNames))
		for i, nodeName := range *result.NodeNames {
			if n, ok := fromNodeName[nodeName]; ok {
				nodeResult[i] = n
			} else {
				return nil, nil, fmt.Errorf(
					"extender %q claims a filtered node %q which is not found in the input node list",
					h.extenderURL, nodeName)
			}
		}
	} else if result.Nodes != nil {
		nodeResult = make([]*v1.Node, len(result.Nodes.Items))
		for i := range result.Nodes.Items {
			nodeResult[i] = &result.Nodes.Items[i]
		}
	}

	return nodeResult, result.FailedNodes, nil
}
```

```
并行优先级过滤
// Prioritize based on extender implemented priority functions. Weight*priority is added
// up for each such priority function. The returned score is added to the score computed
// by Kubernetes scheduler. The total score is used to do the host selection.
func (h *HTTPExtender) Prioritize(pod *v1.Pod, nodes []*v1.Node) (*extenderv1.HostPriorityList, int64, error) {
	var (
		result    extenderv1.HostPriorityList
		nodeList  *v1.NodeList
		nodeNames *[]string
		args      *extenderv1.ExtenderArgs
	)

	if h.prioritizeVerb == "" {
		result := extenderv1.HostPriorityList{}
		for _, node := range nodes {
			result = append(result, extenderv1.HostPriority{Host: node.Name, Score: 0})
		}
		return &result, 0, nil
	}

	if h.nodeCacheCapable {
		nodeNameSlice := make([]string, 0, len(nodes))
		for _, node := range nodes {
			nodeNameSlice = append(nodeNameSlice, node.Name)
		}
		nodeNames = &nodeNameSlice
	} else {
		nodeList = &v1.NodeList{}
		for _, node := range nodes {
			nodeList.Items = append(nodeList.Items, *node)
		}
	}

	args = &extenderv1.ExtenderArgs{
		Pod:       pod,
		Nodes:     nodeList,
		NodeNames: nodeNames,
	}

	if err := h.send(h.prioritizeVerb, args, &result); err != nil {
		return nil, 0, err
	}
	return &result, h.weight, nil
}
```

```
Bind 操作
// Bind delegates the action of binding a pod to a node to the extender.
func (h *HTTPExtender) Bind(binding *v1.Binding) error {
	var result extenderv1.ExtenderBindingResult
	if !h.IsBinder() {
		// This shouldn't happen as this extender wouldn't have become a Binder.
		return fmt.Errorf("Unexpected empty bindVerb in extender")
	}
	req := &extenderv1.ExtenderBindingArgs{
		PodName:      binding.Name,
		PodNamespace: binding.Namespace,
		PodUID:       binding.UID,
		Node:         binding.Target.Name,
	}
	if err := h.send(h.bindVerb, &req, &result); err != nil {
		return err
	}
	if result.Error != "" {
		return fmt.Errorf(result.Error)
	}
	return nil
}
```


## yaml 配置文件

extend-sample-scheduler.yaml

```
apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-scheduler
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: my-scheduler-cluster-admin
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    namespace: kube-system
    name: my-scheduler
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-scheduler-config
  namespace: kube-system
data:
  config.yaml: |
    apiVersion: kubescheduler.config.k8s.io/v1alpha1
    kind: KubeSchedulerConfiguration
    schedulerName: my-scheduler
    algorithmSource:
      policy:
        configMap:
          namespace: kube-system
          name: my-scheduler-policy
    leaderElection:
      leaderElect: true
      lockObjectName: my-scheduler
      lockObjectNamespace: kube-system
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-scheduler-policy
  namespace: kube-system
data:
 policy.cfg : |
  {
    "kind" : "Policy",
    "apiVersion" : "v1",
    "predicates" : [
      {"name" : "PodFitsHostPorts"},
      {"name" : "PodFitsResources"},
      {"name" : "NoDiskConflict"},
      {"name" : "MatchNodeSelector"},
      {"name" : "HostName"}
    ],
    "priorities" : [
      {"name" : "LeastRequestedPriority", "weight" : 1},
      {"name" : "BalancedResourceAllocation", "weight" : 1},
      {"name" : "ServiceSpreadingPriority", "weight" : 1},
      {"name" : "EqualPriority", "weight" : 1}
    ],
    "extenders" : [{
      "urlPrefix": "http://localhost/scheduler",
      "filterVerb": "predicates/TruePredicate",
      "prioritizeVerb": "priorities/ZeroPriority",
      "preemptVerb": "preemption",
      "bindVerb": "",
      "weight": 1,
      "enableHttps": false,
      "nodeCacheCapable": false
    }],
    "hardPodAffinitySymmetricWeight" : 10
  }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-scheduler
  namespace: kube-system
  labels:
    app: my-scheduler
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-scheduler
  template:
    metadata:
      labels:
        app: my-scheduler
    spec:
      serviceAccountName: my-scheduler
      volumes:
      - name: my-scheduler-config
        configMap:
          name: my-scheduler-config
      containers:
      - name: my-scheduler-ctr
        image: gcr.io/google_containers/hyperkube:v1.16.3
        imagePullPolicy: IfNotPresent
        args:
        - kube-scheduler
        - --config=/my-scheduler/config.yaml
        - -v=4
        volumeMounts:
        - name: my-scheduler-config
          mountPath: /my-scheduler
      - name: my-scheduler-extender-ctr
        image: my-scheduler:1.1
        imagePullPolicy: IfNotPresent
        livenessProbe:
          httpGet:
            path: /version
            port: 80
        readinessProbe:
          httpGet:
            path: /version
            port: 80
        ports:
          - containerPort: 80
      nodeSelector: 
        scheduler-node: my-scheduler-master-node
```

test-my-scheduler.yaml

```
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-pod-sheduler
spec:
  replicas: 1
  selector:
    matchLabels:
      name: nginx
  template:
    metadata:
      labels:
        name: nginx
    spec:
      containers:
        - name: nginx
          image: nginx
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 80
      schedulerName: my-scheduler
      nodeSelector: 
        scheduler-node: my-scheduler-master-node
```

test-pod.yaml

```
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  schedulerName: my-scheduler
  containers:
  - name: nginx
    image: nginx
    ports:
    - containerPort: 80
```

其中 extend-sample-scheduler.yaml 与 test-my-scheduler.yaml 指定了 nodeSelector ，将其调度到 k8s-master 节点。

kubectl label node k8s-master scheduler-node=my-scheduler-master-node


## 示例

1. 构建 my-scheduler 镜像

1. 部署调度器 YAML
    
    kubectl -n kube-system logs deploy/my-scheduler -c my-scheduler-extender-ctr -f

1. 部署测试应用 YAML

1. 应用操作

    完成 kubectl apply -f extend-sample-scheduler.yaml 与 kubectl apply -f test-my-scheduler.yaml 操作后
    
    kubectl -n kube-system logs deploy/my-scheduler -c my-scheduler-extender-ctr -f

日志如下：

```
[  warn ] 2020/02/28 05:47:07 main.go:79: LOG_LEVEL="" is empty or invalid, fallling back to "INFO".
[  info ] 2020/02/28 05:47:07 main.go:93: Log level was set to INFO
[  info ] 2020/02/28 05:47:07 main.go:111: server starting on the port : 80
[  info ] 2020/02/28 05:47:09 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 05:47:09 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 05:47:09 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 05:47:09 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 05:47:19 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 05:47:19 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 05:47:19 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 05:47:19 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 05:47:24 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 05:47:24 routes.go:42: =================PredicateRoute=====================
[  info ] 2020/02/28 05:47:24 routes.go:45: TruePredicate ExtenderArgs = 
[  info ] 2020/02/28 05:47:24 routes.go:63: TruePredicate extenderFilterResult = {"Nodes":{"metadata":{},"items":[{"metadata":{"name":"k8s-master","selfLink":"/api/v1/nodes/k8s-master","uid":"7cd46f32-c1cc-428f-9fab-8d1a3922231f","resourceVersion":"154048","creationTimestamp":"2020-02-25T09:57:25Z","labels":{"beta.kubernetes.io/arch":"amd64","beta.kubernetes.io/os":"linux","kubernetes.io/arch":"amd64","kubernetes.io/hostname":"k8s-master","kubernetes.io/os":"linux","node-role.kubernetes.io/master":"","scheduler-node":"my-scheduler-master-node"},"annotations":{"kubeadm.alpha.kubernetes.io/cri-socket":"/var/run/dockershim.sock","node.alpha.kubernetes.io/ttl":"0","projectcalico.org/IPv4Address":"192.168.17.130/24","projectcalico.org/IPv4IPIPTunnelAddr":"192.168.235.192","volumes.kubernetes.io/controller-managed-attach-detach":"true"}},"spec":{"podCIDR":"10.244.0.0/24","podCIDRs":["10.244.0.0/24"]},"status":{"capacity":{"cpu":"2","ephemeral-storage":"49997976Ki","hugepages-1Gi":"0","hugepages-2Mi":"0","memory":"3861356Ki","pods":"110"},"allocatable":{"cpu":"2","ephemeral-storage":"46078134606","hugepages-1Gi":"0","hugepages-2Mi":"0","memory":"3758956Ki","pods":"110"},"conditions":[{"type":"NetworkUnavailable","status":"False","lastHeartbeatTime":"2020-02-27T09:01:52Z","lastTransitionTime":"2020-02-27T09:01:52Z","reason":"CalicoIsUp","message":"Calico is running on this node"},{"type":"MemoryPressure","status":"False","lastHeartbeatTime":"2020-02-28T05:47:08Z","lastTransitionTime":"2020-02-25T09:57:22Z","reason":"KubeletHasSufficientMemory","message":"kubelet has sufficient memory available"},{"type":"DiskPressure","status":"False","lastHeartbeatTime":"2020-02-28T05:47:08Z","lastTransitionTime":"2020-02-25T09:57:22Z","reason":"KubeletHasNoDiskPressure","message":"kubelet has no disk pressure"},{"type":"PIDPressure","status":"False","lastHeartbeatTime":"2020-02-28T05:47:08Z","lastTransitionTime":"2020-02-25T09:57:22Z","reason":"KubeletHasSufficientPID","message":"kubelet has sufficient PID available"},{"type":"Ready","status":"True","lastHeartbeatTime":"2020-02-28T05:47:08Z","lastTransitionTime":"2020-02-25T09:58:15Z","reason":"KubeletReady","message":"kubelet is posting ready status"}],"addresses":[{"type":"InternalIP","address":"192.168.17.130"},{"type":"Hostname","address":"k8s-master"}],"daemonEndpoints":{"kubeletEndpoint":{"Port":10250}},"nodeInfo":{"machineID":"dc92100772ae4164aae8c5d98f937afb","systemUUID":"84194D56-77D1-3802-5C7E-AE1236A8EFA3","bootID":"63b4f94c-b78d-430d-8235-d5efa9e697bb","kernelVersion":"3.10.0-1062.4.3.el7.x86_64","osImage":"CentOS Linux 7 (Core)","containerRuntimeVersion":"docker://19.3.5","kubeletVersion":"v1.16.3","kubeProxyVersion":"v1.16.3","operatingSystem":"linux","architecture":"amd64"},"images":[{"names":["gcr.io/google_containers/hyperkube:v1.16.3"],"sizeBytes":605086222},{"names":["calico/node@sha256:887bcd551668cccae1fbfd6d2eb0f635ec37bb4cf599e1169989aa49dfac5b57","calico/node:v3.11.2"],"sizeBytes":255343962},{"names":["registry.aliyuncs.com/google_containers/etcd@sha256:37a8acab63de5556d47bfbe76d649ae63f83ea7481584a2be0dbffb77825f692","registry.aliyuncs.com/google_containers/etcd:3.3.15-0"],"sizeBytes":246640776},{"names":["calico/node@sha256:8ee677fa0969bf233deb9d9e12b5f2840a0e64b7d6acaaa8ac526672896b8e3c","calico/node:v3.11.1"],"sizeBytes":225848439},{"names":["registry.aliyuncs.com/google_containers/kube-apiserver@sha256:5924223795df919e33e57cb8ada006e0f5d7ebc432cc7d9c56d9dfcf161876b2","registry.aliyuncs.com/google_containers/kube-apiserver:v1.16.3"],"sizeBytes":217075038},{"names":["calico/cni@sha256:f5808401a96ba93010b9693019496d88070dde80dda6976d10bc4328f1f18f4e","calico/cni:v3.11.2"],"sizeBytes":204185753},{"names":["calico/cni@sha256:e493af94c8385fdfbce859dd15e52d35e9bf34a0446fec26514bb1306e323c17","calico/cni:v3.11.1"],"sizeBytes":197763545},{"names":["calico/node@sha256:441abbaeaa2a03d529687f8da49dab892d91ca59f30c000dfb5a0d6a7c2ede24","calico/node:v3.10.1"],"sizeBytes":192029475},{"names":["calico/cni@sha256:dad425d218fd33b23a3929b7e6a31629796942e9e34b13710d7be69cea35cb22","calico/cni:v3.10.1"],"sizeBytes":163333600},{"names":["registry.aliyuncs.com/google_containers/kube-controller-manager@sha256:6a9857c80f7a34e71ab7c8d34581937cd4844014b3f83506696d64d3adcb82b5","registry.aliyuncs.com/google_containers/kube-controller-manager:v1.16.3"],"sizeBytes":163309982},{"names":["nginx@sha256:380eb808e2a3b0dd954f92c1cae2f845e6558a15037efefcabc5b4e03d666d03","nginx:latest"],"sizeBytes":126698311},{"names":["consul@sha256:a167e7222c84687c3e7f392f13b23d9f391cac80b6b839052e58617dab714805","consul:latest"],"sizeBytes":117383703},{"names":["calico/pod2daemon-flexvol@sha256:93c64d6e3e0a0dc75d1b21974db05d28ef2162bd916b00ce62a39fd23594f810","calico/pod2daemon-flexvol:v3.11.2"],"sizeBytes":111122324},{"names":["calico/pod2daemon-flexvol@sha256:4757a518c0cd54d3cad9481c943716ae86f31cdd57008afc7e8820b1821a74b9","calico/pod2daemon-flexvol:v3.11.1"],"sizeBytes":111122324},{"names":["tanjunchen/sample-scheduler:1.0"],"sizeBytes":109863906},{"names":["registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx@sha256:d4db8e0334e213e865fd1933b065f44082ff0c187bbc77da57adb0cfad4494b2","registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx:delay_v1"],"sizeBytes":109057418},{"names":["registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx@sha256:d9b43ba0db2f6a02ce843d9c2d68558e514864dec66d34b9dd82ab9a44f16671","registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx:v1"],"sizeBytes":109057403},{"names":["registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx@sha256:31da21eb24615dacdd8d219e1d43dcbaea6d78f6f8548ce50c3f56699312496b","registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx:v2"],"sizeBytes":109057403},{"names":["registry.aliyuncs.com/google_containers/kube-scheduler@sha256:3c12503e1a5acb2e078eeabfee746972d49ca31e70d9267f37078596b56c281c","registry.aliyuncs.com/google_containers/kube-scheduler:v1.16.3"],"sizeBytes":87274014},{"names":["registry.aliyuncs.com/google_containers/kube-proxy@sha256:1a1b21354e31190c0a1c6b0e16485ec095e7a4d423620e4381c3982ebfa24b3a","registry.aliyuncs.com/google_containers/kube-proxy:v1.16.3"],"sizeBytes":86065116},{"names":["my-scheduler:1.0"],"sizeBytes":74202453},{"names":["my-scheduler:1.1"],"sizeBytes":74202453},{"names":["quay.io/coreos/flannel@sha256:3fa662e491a5e797c789afbd6d5694bdd186111beb7b5c9d66655448a7d3ae37","quay.io/coreos/flannel:v0.11.0"],"sizeBytes":52567296},{"names":["calico/kube-controllers@sha256:46951fa7f713dfb0acc6be5edb82597df6f31ddc4e25c4bc9db889e894d02dd7","calico/kube-controllers:v3.11.1"],"sizeBytes":52477980},{"names":["calico/kube-controllers@sha256:1169cca40b489271714cb1e97fed9b6b198aabdca1a1cc61698dd73ee6703d60","calico/kube-controllers:v3.11.2"],"sizeBytes":52477980},{"names":["calico/kube-controllers@sha256:7ffbd22607d497a8f1d0e449a3d347f9ae4d32ef0f48fb0b7504408538d53c38","calico/kube-controllers:v3.10.1"],"sizeBytes":50634129},{"names":["registry.aliyuncs.com/google_containers/coredns@sha256:4dd4d0e5bcc9bd0e8189f6fa4d4965ffa81207d8d99d29391f28cbd1a70a0163","registry.aliyuncs.com/google_containers/coredns:1.6.2"],"sizeBytes":44100963},{"names":["kubernetesui/metrics-scraper@sha256:35fcae4fd9232a541a8cb08f2853117ba7231750b75c2cb3b6a58a2aaa57f878","kubernetesui/metrics-scraper:v1.0.1"],"sizeBytes":40101504},{"names":["calico/pod2daemon-flexvol@sha256:42ca53c5e4184ac859f744048e6c3d50b0404b9a73a9c61176428be5026844fe","calico/pod2daemon-flexvol:v3.10.1"],"sizeBytes":9780495},{"names":["registry.aliyuncs.com/google_containers/pause@sha256:759c3f0f6493093a9043cc813092290af69029699ade0e3dbe024e968fcb7cca","registry.aliyuncs.com/google_containers/pause:3.1"],"sizeBytes":742472}]}}]},"NodeNames":null,"FailedNodes":{},"Error":""}
[  info ] 2020/02/28 05:47:24 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 05:47:29 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 05:47:29 routes.go:179: =================debugLogging=====================
```

执行 kubectl apply -f test-pod.yaml 操作

```
[  info ] 2020/02/28 06:22:39 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 06:22:39 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 06:22:39 routes.go:42: =================PredicateRoute=====================
[  info ] 2020/02/28 06:22:39 routes.go:45: TruePredicate ExtenderArgs = 
[  info ] 2020/02/28 06:22:39 routes.go:63: TruePredicate extenderFilterResult = {"Nodes":{"metadata":{},"items":[{"metadata":{"name":"k8s-master","selfLink":"/api/v1/nodes/k8s-master","uid":"7cd46f32-c1cc-428f-9fab-8d1a3922231f","resourceVersion":"157931","creationTimestamp":"2020-02-25T09:57:25Z","labels":{"beta.kubernetes.io/arch":"amd64","beta.kubernetes.io/os":"linux","kubernetes.io/arch":"amd64","kubernetes.io/hostname":"k8s-master","kubernetes.io/os":"linux","node-role.kubernetes.io/master":"","scheduler-node":"my-scheduler-master-node"},"annotations":{"kubeadm.alpha.kubernetes.io/cri-socket":"/var/run/dockershim.sock","node.alpha.kubernetes.io/ttl":"0","projectcalico.org/IPv4Address":"192.168.17.130/24","projectcalico.org/IPv4IPIPTunnelAddr":"192.168.235.192","volumes.kubernetes.io/controller-managed-attach-detach":"true"}},"spec":{"podCIDR":"10.244.0.0/24","podCIDRs":["10.244.0.0/24"]},"status":{"capacity":{"cpu":"2","ephemeral-storage":"49997976Ki","hugepages-1Gi":"0","hugepages-2Mi":"0","memory":"3861356Ki","pods":"110"},"allocatable":{"cpu":"2","ephemeral-storage":"46078134606","hugepages-1Gi":"0","hugepages-2Mi":"0","memory":"3758956Ki","pods":"110"},"conditions":[{"type":"NetworkUnavailable","status":"False","lastHeartbeatTime":"2020-02-27T09:01:52Z","lastTransitionTime":"2020-02-27T09:01:52Z","reason":"CalicoIsUp","message":"Calico is running on this node"},{"type":"MemoryPressure","status":"False","lastHeartbeatTime":"2020-02-28T06:22:15Z","lastTransitionTime":"2020-02-25T09:57:22Z","reason":"KubeletHasSufficientMemory","message":"kubelet has sufficient memory available"},{"type":"DiskPressure","status":"False","lastHeartbeatTime":"2020-02-28T06:22:15Z","lastTransitionTime":"2020-02-25T09:57:22Z","reason":"KubeletHasNoDiskPressure","message":"kubelet has no disk pressure"},{"type":"PIDPressure","status":"False","lastHeartbeatTime":"2020-02-28T06:22:15Z","lastTransitionTime":"2020-02-25T09:57:22Z","reason":"KubeletHasSufficientPID","message":"kubelet has sufficient PID available"},{"type":"Ready","status":"True","lastHeartbeatTime":"2020-02-28T06:22:15Z","lastTransitionTime":"2020-02-25T09:58:15Z","reason":"KubeletReady","message":"kubelet is posting ready status"}],"addresses":[{"type":"InternalIP","address":"192.168.17.130"},{"type":"Hostname","address":"k8s-master"}],"daemonEndpoints":{"kubeletEndpoint":{"Port":10250}},"nodeInfo":{"machineID":"dc92100772ae4164aae8c5d98f937afb","systemUUID":"84194D56-77D1-3802-5C7E-AE1236A8EFA3","bootID":"63b4f94c-b78d-430d-8235-d5efa9e697bb","kernelVersion":"3.10.0-1062.4.3.el7.x86_64","osImage":"CentOS Linux 7 (Core)","containerRuntimeVersion":"docker://19.3.5","kubeletVersion":"v1.16.3","kubeProxyVersion":"v1.16.3","operatingSystem":"linux","architecture":"amd64"},"images":[{"names":["gcr.io/google_containers/hyperkube:v1.16.3"],"sizeBytes":605086222},{"names":["calico/node@sha256:887bcd551668cccae1fbfd6d2eb0f635ec37bb4cf599e1169989aa49dfac5b57","calico/node:v3.11.2"],"sizeBytes":255343962},{"names":["registry.aliyuncs.com/google_containers/etcd@sha256:37a8acab63de5556d47bfbe76d649ae63f83ea7481584a2be0dbffb77825f692","registry.aliyuncs.com/google_containers/etcd:3.3.15-0"],"sizeBytes":246640776},{"names":["calico/node@sha256:8ee677fa0969bf233deb9d9e12b5f2840a0e64b7d6acaaa8ac526672896b8e3c","calico/node:v3.11.1"],"sizeBytes":225848439},{"names":["registry.aliyuncs.com/google_containers/kube-apiserver@sha256:5924223795df919e33e57cb8ada006e0f5d7ebc432cc7d9c56d9dfcf161876b2","registry.aliyuncs.com/google_containers/kube-apiserver:v1.16.3"],"sizeBytes":217075038},{"names":["calico/cni@sha256:f5808401a96ba93010b9693019496d88070dde80dda6976d10bc4328f1f18f4e","calico/cni:v3.11.2"],"sizeBytes":204185753},{"names":["calico/cni@sha256:e493af94c8385fdfbce859dd15e52d35e9bf34a0446fec26514bb1306e323c17","calico/cni:v3.11.1"],"sizeBytes":197763545},{"names":["calico/node@sha256:441abbaeaa2a03d529687f8da49dab892d91ca59f30c000dfb5a0d6a7c2ede24","calico/node:v3.10.1"],"sizeBytes":192029475},{"names":["calico/cni@sha256:dad425d218fd33b23a3929b7e6a31629796942e9e34b13710d7be69cea35cb22","calico/cni:v3.10.1"],"sizeBytes":163333600},{"names":["registry.aliyuncs.com/google_containers/kube-controller-manager@sha256:6a9857c80f7a34e71ab7c8d34581937cd4844014b3f83506696d64d3adcb82b5","registry.aliyuncs.com/google_containers/kube-controller-manager:v1.16.3"],"sizeBytes":163309982},{"names":["nginx@sha256:380eb808e2a3b0dd954f92c1cae2f845e6558a15037efefcabc5b4e03d666d03","nginx:latest"],"sizeBytes":126698311},{"names":["consul@sha256:a167e7222c84687c3e7f392f13b23d9f391cac80b6b839052e58617dab714805","consul:latest"],"sizeBytes":117383703},{"names":["calico/pod2daemon-flexvol@sha256:93c64d6e3e0a0dc75d1b21974db05d28ef2162bd916b00ce62a39fd23594f810","calico/pod2daemon-flexvol:v3.11.2"],"sizeBytes":111122324},{"names":["calico/pod2daemon-flexvol@sha256:4757a518c0cd54d3cad9481c943716ae86f31cdd57008afc7e8820b1821a74b9","calico/pod2daemon-flexvol:v3.11.1"],"sizeBytes":111122324},{"names":["tanjunchen/sample-scheduler:1.0"],"sizeBytes":109863906},{"names":["registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx@sha256:d4db8e0334e213e865fd1933b065f44082ff0c187bbc77da57adb0cfad4494b2","registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx:delay_v1"],"sizeBytes":109057418},{"names":["registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx@sha256:d9b43ba0db2f6a02ce843d9c2d68558e514864dec66d34b9dd82ab9a44f16671","registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx:v1"],"sizeBytes":109057403},{"names":["registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx@sha256:31da21eb24615dacdd8d219e1d43dcbaea6d78f6f8548ce50c3f56699312496b","registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx:v2"],"sizeBytes":109057403},{"names":["registry.aliyuncs.com/google_containers/kube-scheduler@sha256:3c12503e1a5acb2e078eeabfee746972d49ca31e70d9267f37078596b56c281c","registry.aliyuncs.com/google_containers/kube-scheduler:v1.16.3"],"sizeBytes":87274014},{"names":["registry.aliyuncs.com/google_containers/kube-proxy@sha256:1a1b21354e31190c0a1c6b0e16485ec095e7a4d423620e4381c3982ebfa24b3a","registry.aliyuncs.com/google_containers/kube-proxy:v1.16.3"],"sizeBytes":86065116},{"names":["my-scheduler:1.0"],"sizeBytes":74202453},{"names":["my-scheduler:1.1"],"sizeBytes":74202453},{"names":["quay.io/coreos/flannel@sha256:3fa662e491a5e797c789afbd6d5694bdd186111beb7b5c9d66655448a7d3ae37","quay.io/coreos/flannel:v0.11.0"],"sizeBytes":52567296},{"names":["calico/kube-controllers@sha256:46951fa7f713dfb0acc6be5edb82597df6f31ddc4e25c4bc9db889e894d02dd7","calico/kube-controllers:v3.11.1"],"sizeBytes":52477980},{"names":["calico/kube-controllers@sha256:1169cca40b489271714cb1e97fed9b6b198aabdca1a1cc61698dd73ee6703d60","calico/kube-controllers:v3.11.2"],"sizeBytes":52477980},{"names":["calico/kube-controllers@sha256:7ffbd22607d497a8f1d0e449a3d347f9ae4d32ef0f48fb0b7504408538d53c38","calico/kube-controllers:v3.10.1"],"sizeBytes":50634129},{"names":["registry.aliyuncs.com/google_containers/coredns@sha256:4dd4d0e5bcc9bd0e8189f6fa4d4965ffa81207d8d99d29391f28cbd1a70a0163","registry.aliyuncs.com/google_containers/coredns:1.6.2"],"sizeBytes":44100963},{"names":["kubernetesui/metrics-scraper@sha256:35fcae4fd9232a541a8cb08f2853117ba7231750b75c2cb3b6a58a2aaa57f878","kubernetesui/metrics-scraper:v1.0.1"],"sizeBytes":40101504},{"names":["calico/pod2daemon-flexvol@sha256:42ca53c5e4184ac859f744048e6c3d50b0404b9a73a9c61176428be5026844fe","calico/pod2daemon-flexvol:v3.10.1"],"sizeBytes":9780495},{"names":["registry.aliyuncs.com/google_containers/pause@sha256:759c3f0f6493093a9043cc813092290af69029699ade0e3dbe024e968fcb7cca","registry.aliyuncs.com/google_containers/pause:3.1"],"sizeBytes":742472}]}},{"metadata":{"name":"node01","selfLink":"/api/v1/nodes/node01","uid":"58701886-88a7-4c02-842c-1ab817807d65","resourceVersion":"157891","creationTimestamp":"2020-02-25T09:59:57Z","labels":{"beta.kubernetes.io/arch":"amd64","beta.kubernetes.io/os":"linux","kubernetes.io/arch":"amd64","kubernetes.io/hostname":"node01","kubernetes.io/os":"linux"},"annotations":{"kubeadm.alpha.kubernetes.io/cri-socket":"/var/run/dockershim.sock","node.alpha.kubernetes.io/ttl":"0","projectcalico.org/IPv4Address":"192.168.17.131/24","projectcalico.org/IPv4IPIPTunnelAddr":"192.168.196.128","volumes.kubernetes.io/controller-managed-attach-detach":"true"}},"spec":{"podCIDR":"10.244.1.0/24","podCIDRs":["10.244.1.0/24"]},"status":{"capacity":{"cpu":"1","ephemeral-storage":"39517336Ki","hugepages-1Gi":"0","hugepages-2Mi":"0","memory":"3861328Ki","pods":"110"},"allocatable":{"cpu":"1","ephemeral-storage":"36419176798","hugepages-1Gi":"0","hugepages-2Mi":"0","memory":"3758928Ki","pods":"110"},"conditions":[{"type":"NetworkUnavailable","status":"False","lastHeartbeatTime":"2020-02-27T09:03:21Z","lastTransitionTime":"2020-02-27T09:03:21Z","reason":"CalicoIsUp","message":"Calico is running on this node"},{"type":"MemoryPressure","status":"False","lastHeartbeatTime":"2020-02-28T06:21:53Z","lastTransitionTime":"2020-02-27T09:03:14Z","reason":"KubeletHasSufficientMemory","message":"kubelet has sufficient memory available"},{"type":"DiskPressure","status":"False","lastHeartbeatTime":"2020-02-28T06:21:53Z","lastTransitionTime":"2020-02-27T09:03:14Z","reason":"KubeletHasNoDiskPressure","message":"kubelet has no disk pressure"},{"type":"PIDPressure","status":"False","lastHeartbeatTime":"2020-02-28T06:21:53Z","lastTransitionTime":"2020-02-27T09:03:14Z","reason":"KubeletHasSufficientPID","message":"kubelet has sufficient PID available"},{"type":"Ready","status":"True","lastHeartbeatTime":"2020-02-28T06:21:53Z","lastTransitionTime":"2020-02-27T09:03:14Z","reason":"KubeletReady","message":"kubelet is posting ready status"}],"addresses":[{"type":"InternalIP","address":"192.168.17.131"},{"type":"Hostname","address":"node01"}],"daemonEndpoints":{"kubeletEndpoint":{"Port":10250}},"nodeInfo":{"machineID":"ab6d300e8c894a008f4dfaacfc65dded","systemUUID":"D2BD4D56-6D5C-9889-59E5-EAD6D167587D","bootID":"4098164f-93a6-4b23-abae-889d79289ca4","kernelVersion":"3.10.0-1062.4.3.el7.x86_64","osImage":"CentOS Linux 7 (Core)","containerRuntimeVersion":"docker://19.3.5","kubeletVersion":"v1.16.3","kubeProxyVersion":"v1.16.3","operatingSystem":"linux","architecture":"amd64"},"images":[{"names":["gcr.io/google_containers/hyperkube:v1.16.3"],"sizeBytes":605086222},{"names":["tomcat@sha256:e895bcbfa20cf4f3f19ca11451dabc166fc8e827dfad9dd714ecaa8c065a3b18","tomcat:8","tomcat:latest"],"sizeBytes":528672323},{"names":["mysql@sha256:6d0741319b6a2ae22c384a97f4bbee411b01e75f6284af0cce339fee83d7e314","mysql:latest"],"sizeBytes":465244873},{"names":["kubeguide/tomcat-app@sha256:7a9193c2e5c6c74b4ad49a8abbf75373d4ab76c8f8db87672dc526b96ac69ac4","kubeguide/tomcat-app:v1"],"sizeBytes":358241257},{"names":["mysql@sha256:bef096aee20d73cbfd87b02856321040ab1127e94b707b41927804776dca02fc","mysql:5.6"],"sizeBytes":302490673},{"names":["calico/node@sha256:887bcd551668cccae1fbfd6d2eb0f635ec37bb4cf599e1169989aa49dfac5b57","calico/node:v3.11.2"],"sizeBytes":255343962},{"names":["calico/node@sha256:8ee677fa0969bf233deb9d9e12b5f2840a0e64b7d6acaaa8ac526672896b8e3c","calico/node:v3.11.1"],"sizeBytes":225848439},{"names":["calico/cni@sha256:f5808401a96ba93010b9693019496d88070dde80dda6976d10bc4328f1f18f4e","calico/cni:v3.11.2"],"sizeBytes":204185753},{"names":["calico/cni@sha256:e493af94c8385fdfbce859dd15e52d35e9bf34a0446fec26514bb1306e323c17","calico/cni:v3.11.1"],"sizeBytes":197763545},{"names":["calico/node@sha256:441abbaeaa2a03d529687f8da49dab892d91ca59f30c000dfb5a0d6a7c2ede24","calico/node:v3.10.1"],"sizeBytes":192029475},{"names":["nginx@sha256:f2d384a6ca8ada733df555be3edc427f2e5f285ebf468aae940843de8cf74645","nginx:1.11.9"],"sizeBytes":181819831},{"names":["nginx@sha256:35779791c05d119df4fe476db8f47c0bee5943c83eba5656a15fc046db48178b","nginx:1.10.1"],"sizeBytes":180708613},{"names":["httpd@sha256:ac6594daaa934c4c6ba66c562e96f2fb12f871415a9b7117724c52687080d35d","httpd:latest"],"sizeBytes":165254767},{"names":["calico/cni@sha256:dad425d218fd33b23a3929b7e6a31629796942e9e34b13710d7be69cea35cb22","calico/cni:v3.10.1"],"sizeBytes":163333600},{"names":["nginx@sha256:380eb808e2a3b0dd954f92c1cae2f845e6558a15037efefcabc5b4e03d666d03","nginx:latest"],"sizeBytes":126698311},{"names":["nginx@sha256:50cf965a6e08ec5784009d0fccb380fc479826b6e0e65684d9879170a9df8566"],"sizeBytes":126323486},{"names":["calico/pod2daemon-flexvol@sha256:93c64d6e3e0a0dc75d1b21974db05d28ef2162bd916b00ce62a39fd23594f810","calico/pod2daemon-flexvol:v3.11.2"],"sizeBytes":111122324},{"names":["calico/pod2daemon-flexvol@sha256:4757a518c0cd54d3cad9481c943716ae86f31cdd57008afc7e8820b1821a74b9","calico/pod2daemon-flexvol:v3.11.1"],"sizeBytes":111122324},{"names":["tanjunchen/sample-scheduler:1.0"],"sizeBytes":109863906},{"names":["nginx@sha256:23b4dcdf0d34d4a129755fc6f52e1c6e23bb34ea011b315d87e193033bcd1b68","nginx:1.15"],"sizeBytes":109331233},{"names":["nginx@sha256:f7988fb6c02e0ce69257d9bd9cf37ae20a60f1df7563c3a2a6abe24160306b8d","nginx:1.14"],"sizeBytes":109129446},{"names":["registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx@sha256:d9b43ba0db2f6a02ce843d9c2d68558e514864dec66d34b9dd82ab9a44f16671","registry.cn-beijing.aliyuncs.com/mrvolleyball/nginx:v1"],"sizeBytes":109057403},{"names":["nginx@sha256:e3456c851a152494c3e4ff5fcc26f240206abac0c9d794affb40e0714846c451","nginx:1.7.9"],"sizeBytes":91664166},{"names":["kubernetesui/dashboard@sha256:fc90baec4fb62b809051a3227e71266c0427240685139bbd5673282715924ea7","kubernetesui/dashboard:v2.0.0-beta8"],"sizeBytes":90835427},{"names":["registry.aliyuncs.com/google_containers/kube-proxy@sha256:1a1b21354e31190c0a1c6b0e16485ec095e7a4d423620e4381c3982ebfa24b3a","registry.aliyuncs.com/google_containers/kube-proxy:v1.16.3"],"sizeBytes":86065116},{"names":["my-scheduler:1.0"],"sizeBytes":74202453},{"names":["quay.io/coreos/flannel@sha256:3fa662e491a5e797c789afbd6d5694bdd186111beb7b5c9d66655448a7d3ae37","quay.io/coreos/flannel:v0.11.0"],"sizeBytes":52567296},{"names":["calico/kube-controllers@sha256:1169cca40b489271714cb1e97fed9b6b198aabdca1a1cc61698dd73ee6703d60","calico/kube-controllers:v3.11.2"],"sizeBytes":52477980},{"names":["calico/kube-controllers@sha256:7ffbd22607d497a8f1d0e449a3d347f9ae4d32ef0f48fb0b7504408538d53c38","calico/kube-controllers:v3.10.1"],"sizeBytes":50634129},{"names":["registry.aliyuncs.com/google_containers/coredns@sha256:4dd4d0e5bcc9bd0e8189f6fa4d4965ffa81207d8d99d29391f28cbd1a70a0163","registry.aliyuncs.com/google_containers/coredns:1.6.2"],"sizeBytes":44100963},{"names":["redis@sha256:e9083e10f5f81d350a3f687d582aefd06e114890b03e7f08a447fa1a1f66d967","redis:3.2-alpine"],"sizeBytes":22894256},{"names":["tutum/hello-world@sha256:0d57def8055178aafb4c7669cbc25ec17f0acdab97cc587f30150802da8f8d85","tutum/hello-world:latest"],"sizeBytes":17791668},{"names":["nginx@sha256:db5acc22920799fe387a903437eb89387607e5b3f63cf0f4472ac182d7bad644","nginx:1.12-alpine"],"sizeBytes":15502679},{"names":["calico/pod2daemon-flexvol@sha256:42ca53c5e4184ac859f744048e6c3d50b0404b9a73a9c61176428be5026844fe","calico/pod2daemon-flexvol:v3.10.1"],"sizeBytes":9780495},{"names":["busybox@sha256:6915be4043561d64e0ab0f8f098dc2ac48e077fe23f488ac24b665166898115a","busybox:latest"],"sizeBytes":1219782},{"names":["gcr.azk8s.cn/google_containers/pause@sha256:f78411e19d84a252e53bff71a4407a5686c46983a2c2eeed83929b888179acea","registry.aliyuncs.com/google_containers/pause@sha256:759c3f0f6493093a9043cc813092290af69029699ade0e3dbe024e968fcb7cca","gcr.azk8s.cn/google_containers/pause:3.1","registry.aliyuncs.com/google_containers/pause:3.1"],"sizeBytes":742472}]}}]},"NodeNames":null,"FailedNodes":{},"Error":""}
[  info ] 2020/02/28 06:22:39 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 06:22:39 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 06:22:39 routes.go:74: =================PrioritizeRoute=====================
[  info ] 2020/02/28 06:22:39 routes.go:78: ZeroPriority ExtenderArgs = 
[  info ] 2020/02/28 06:22:39 routes.go:96: ZeroPriority hostPriorityList = [{"Host":"k8s-master","Score":0},{"Host":"node01","Score":0}]
[  info ] 2020/02/28 06:22:39 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 06:22:49 routes.go:175: =================debugLogging=====================
[  info ] 2020/02/28 06:22:49 routes.go:179: =================debugLogging=====================
[  info ] 2020/02/28 06:22:49 routes.go:175: =================debugLogging=====================
```

## 注意事项

extend-sample-scheduler.yaml 中的镜像 image: my-scheduler:1.1

image: gcr.io/google_containers/hyperkube:v1.16.3  (网络可达)

## 参考

[k8s-scheduler-extender-example](https://github.com/everpeace/k8s-scheduler-extender-example)

[K8s Scheduler 调度器](https://zhuanlan.zhihu.com/p/101908480)
