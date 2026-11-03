package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/bolubo/checkpoint-governor/internal/permit"
	"github.com/bolubo/checkpoint-governor/internal/produce/adapters"
)

// runSubmit is the whole gate in one process: read the world, decide who pays, hold the arrival,
// watch for the seal, then create the job.
func runSubmit(args []string) int {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "the Job manifest to submit (required)")
	worldDir := fs.String("world", "", "the world directory the observer writes (required)")
	cluster := fs.String("cluster", "", "the cluster name the observer writes under (default: the kube context)")
	queue := fs.String("queue", "", "the ClusterQueue this arrival goes to (required)")
	ckptRoot := fs.String("ckpt-root", "", "where the victims' checkpoint directories live (required)")
	deadline := fs.Duration("deadline", 2*time.Minute, "how long the gate waits for the victims to seal")
	namespace := fs.String("namespace", "", "namespace when the manifest names none")
	nodeName := fs.String("node", "", "hold a victim on this node (default: the emptiest node)")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: the usual locations, then in-cluster)")
	dryRun := fs.Bool("dry-run", false, "print the victims and the wait, change nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" || *queue == "" {
		fmt.Fprintln(os.Stderr, "ckptgate submit: --manifest and --queue are required")
		return 2
	}
	out := os.Stdout

	job, err := loadJob(*manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ckptgate submit: %v\n", err)
		return 1
	}
	if *namespace != "" && job.Namespace == "" {
		job.Namespace = *namespace
	}
	if job.Namespace == "" {
		job.Namespace = "default"
	}

	cfg, ctxName, err := kubeConfig(*kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ckptgate submit: %v\n", err)
		return 1
	}
	clusterName := *cluster
	if clusterName == "" {
		clusterName = ctxName
	}
	if *worldDir == "" {
		fmt.Fprintln(os.Stderr, "ckptgate submit: --world is required")
		return 2
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ckptgate submit: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The arrival's effective priority: a value in the pod spec wins; otherwise the class is resolved
	// here. A zero priority is below nothing, and a job that outranks nobody cannot take anybody.
	prio := priorityOf(job)
	if job.Spec.Template.Spec.Priority == nil && job.Spec.Template.Spec.PriorityClassName != "" {
		pc, err := client.SchedulingV1().PriorityClasses().Get(ctx, job.Spec.Template.Spec.PriorityClassName, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ckptgate submit: priority class %q: %v\n", job.Spec.Template.Spec.PriorityClassName, err)
			return 1
		}
		prio = pc.Value
	}
	need := needOf(job)
	if prio <= 0 || need <= 0 {
		// Nothing to gate: the arrival outranks nobody or asks for nothing. Submitting it directly is
		// the honest answer, and the alternative would be a hold with no arithmetic behind it.
		return createJob(client, job, out, "no priority or no request: submitted directly")
	}

	summary, err := adapters.ReadSummary(*worldDir, clusterName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return createJob(client, job, out, "no world for cluster "+clusterName+": submitted without a hold")
		}
		fmt.Fprintf(os.Stderr, "ckptgate submit: %v\n", err)
		return 1
	}
	now := time.Now()
	if summary.Stale(now) {
		return createJob(client, job, out, "the world is stale: submitted without a hold")
	}
	q, ok := queueByName(summary, *queue)
	if !ok {
		return createJob(client, job, out, "queue "+*queue+" is not in the world: submitted without a hold")
	}

	node, nodeFree := *nodeName, int64(0)
	if node == "" {
		node, nodeFree = pickNode(summary.Nodes)
	} else if n, ok := summary.Node(node); ok {
		nodeFree = n.Allocatable.CPUMilli - n.Requested.CPUMilli
	} else {
		fmt.Fprintf(os.Stderr, "ckptgate submit: node %q is not in the world\n", node)
		return 1
	}
	if node == "" {
		return createJob(client, job, out, "no node is readable: submitted without a hold")
	}
	shortfall, pressing := shortfallFor(q, nodeFree, need)
	if !pressing {
		return createJob(client, job, out, "the queue is not short on "+node+": submitted without a hold")
	}

	units, err := readUnits(*worldDir, clusterName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ckptgate submit: %v\n", err)
		return 1
	}
	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ckptgate submit: list pods: %v\n", err)
		return 1
	}
	// The size of a victim is a fact about its pod, and the world deliberately does not carry it: a
	// unit record is "who could die", not how much room it holds. One LIST at submit time is cheap
	// because submissions are rare.
	dirs, err := governedDirs(*ckptRoot, units)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ckptgate submit: %v\n", err)
		return 1
	}
	cands := candidates(units, cpuSizes(pods.Items), dirs, node, prio)
	victims := permit.Victims(cands, permit.CauseReclaim, int(shortfall))
	if len(victims) == 0 {
		return createJob(client, job, out,
			"nobody below this priority on "+node+" is governed here: submitted without a hold")
	}
	dirs = map[string]string{}
	for _, v := range victims {
		if d, ok := findUnitDir(*ckptRoot, v.ID); ok {
			dirs[v.ID] = d
		}
	}
	if len(dirs) != len(victims) {
		return createJob(client, job, out, "a victim's checkpoint dir disappeared: submitted without a hold")
	}

	names := make([]string, 0, len(victims))
	for _, v := range victims {
		names = append(names, fmt.Sprintf("%s (priority %d, size %dm)", v.ID, v.Priority, v.Size))
	}
	fmt.Fprintf(out, "gate       %s/%s needs %dm; %s is %dm short on %s\n",
		job.Namespace, jobName(job), need, *queue, shortfall, node)
	fmt.Fprintf(out, "victims    %s\n", joinStrings(names, ", "))
	fmt.Fprintf(out, "deadline   %s\n", deadline.Round(time.Second))
	if *dryRun {
		fmt.Fprintln(out, "dry-run    nothing was held and nothing was created")
		return 0
	}

	since := time.Now()
	requestID := reclaimID(job)
	for i, v := range victims {
		rec := adapters.ReclaimRecord{
			UnitID: v.ID, RequestID: requestID, Intruder: job.Namespace + "/" + jobName(job),
			NeedMilli: need, State: adapters.ReclaimHeld, Since: since, Deadline: since.Add(*deadline),
		}
		if err := adapters.WriteReclaim(*worldDir, clusterName, rec); err != nil {
			fmt.Fprintf(os.Stderr, "ckptgate submit: write reclaim: %v\n", err)
			return 1
		}
		// The permit is the fleet's answer, and the gate is the fleet that knows: this unit is the one
		// the reclaim selects, so it may spend the write (0021).
		if err := grant(*worldDir, clusterName, v.ID, i+1); err != nil {
			fmt.Fprintf(os.Stderr, "ckptgate submit: grant %s: %v\n", v.ID, err)
			return 1
		}
	}

	step, ok := waitForSeals(dirs, since, *deadline)
	elapsed := time.Since(since).Round(100 * time.Millisecond)
	state := adapters.ReclaimReleased
	if !ok {
		// The deadline is the fail-open edge: the arrival goes in anyway, and the line says what the
		// gate could not buy. A hold that outlives its own promise is worse than no hold.
		fmt.Fprintf(out, "gate       waited %s and no seal: releasing the arrival (the write was not covered)\n", elapsed)
	}
	for _, v := range victims {
		rec := adapters.ReclaimRecord{
			UnitID: v.ID, RequestID: requestID, Intruder: job.Namespace + "/" + jobName(job),
			NeedMilli: need, State: state, Since: since, Deadline: since.Add(*deadline),
		}
		if err := adapters.WriteReclaim(*worldDir, clusterName, rec); err != nil {
			fmt.Fprintf(os.Stderr, "ckptgate submit: release %s: %v\n", v.ID, err)
			return 1
		}
	}
	if ok {
		fmt.Fprintf(out, "gate       sealed at step %d after %s: releasing the arrival\n", step, elapsed)
	}
	return createJob(client, job, out, "")
}

func createJob(client kubernetes.Interface, job *batchv1.Job, out io.Writer, why string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if job.Name == "" && job.GenerateName == "" {
		fmt.Fprintln(os.Stderr, "ckptgate submit: the manifest has neither a name nor a generateName")
		return 1
	}
	created, err := client.BatchV1().Jobs(job.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ckptgate submit: create job: %v\n", err)
		return 1
	}
	if why != "" {
		fmt.Fprintf(out, "gate       %s\n", why)
	}
	fmt.Fprintf(out, "submitted  %s/%s\n", created.Namespace, created.Name)
	return 0
}

// grant writes the permit that selects this unit for the reclaim. The unit record is read first so
// every other field the observer wrote survives: the gate is a second writer of one field, not a
// replacement of the world.
func grant(worldDir, cluster, unitID string, rank int) error {
	u, err := adapters.ReadUnit(worldDir, cluster, unitID)
	if err != nil {
		return err
	}
	u.Permit = &adapters.UnitPermit{Verdict: string(permit.Granted), Cause: string(permit.CauseReclaim), Rank: rank}
	return adapters.WriteUnit(worldDir, cluster, u)
}

func loadJob(path string) (*batchv1.Job, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var job batchv1.Job
	if err := yaml.Unmarshal(raw, &job); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if job.Kind != "Job" || (job.APIVersion != "" && job.APIVersion != "batch/v1") {
		return nil, fmt.Errorf("%s: want a batch/v1 Job, got %q %q", filepath.Base(path), job.APIVersion, job.Kind)
	}
	return &job, nil
}

func queueByName(s adapters.WorldSummary, name string) (adapters.QueueWire, bool) {
	for _, q := range s.Queues {
		if q.Name == name {
			return q, true
		}
	}
	return adapters.QueueWire{}, false
}

// governedDirs maps every unit the gate can actually hold to its checkpoint directory. A unit without
// one is not governed here: it may be a system pod the observer recorded, or a job whose volume is
// not mounted where --ckpt-root points. Refusing to guess which is which is the point.
func governedDirs(root string, units []adapters.UnitRecord) (map[string]string, error) {
	if root == "" {
		return nil, errors.New("--ckpt-root is required to choose a victim")
	}
	out := make(map[string]string, len(units))
	for _, u := range units {
		if d, ok := findUnitDir(root, u.UnitID); ok {
			out[u.UnitID] = d
		}
	}
	return out, nil
}

func readUnits(worldDir, cluster string) ([]adapters.UnitRecord, error) {
	dir := filepath.Join(worldDir, cluster, "units")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []adapters.UnitRecord
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var u adapters.UnitRecord
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, fmt.Errorf("unit %s: %w", e.Name(), err)
		}
		out = append(out, u)
	}
	return out, nil
}

// waitForSeals polls until every victim has a manifest newer than the hold, or the deadline passes.
// The poll is a few stats; the victims' own controllers decide when to write, and the gate only
// watches the directory the contract already defines.
func waitForSeals(dirs map[string]string, since time.Time, deadline time.Duration) (int64, bool) {
	end := since.Add(deadline)
	var step int64
	for {
		done := true
		for _, dir := range dirs {
			at, s, ok := newestManifest(dir, since)
			if !ok || !at.After(since) {
				done = false
				break
			}
			if s > step {
				step = s
			}
		}
		if done && len(dirs) > 0 {
			return step, true
		}
		if time.Now().After(end) {
			return 0, false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func kubeConfig(path string) (*rest.Config, string, error) {
	if path != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		raw, _ := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: path},
			&clientcmd.ConfigOverrides{}).RawConfig()
		return cfg, ctxOr(raw.CurrentContext, "cluster"), err
	}
	if c, err := rest.InClusterConfig(); err == nil {
		return c, "in-cluster", nil
	}
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	client := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, &clientcmd.ConfigOverrides{})
	c, err := client.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("no cluster credentials: %w", err)
	}
	raw, _ := client.RawConfig()
	return c, ctxOr(raw.CurrentContext, "cluster"), nil
}

func ctxOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

func jobName(job *batchv1.Job) string {
	if job.Name != "" {
		return job.Name
	}
	return job.GenerateName + "*"
}

func reclaimID(job *batchv1.Job) string {
	return fmt.Sprintf("gate:%s:%d", jobName(job), time.Now().UnixNano())
}

func joinStrings(values []string, sep string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}
