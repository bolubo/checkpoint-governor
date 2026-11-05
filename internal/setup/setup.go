// Package setup answers "can this cluster run a governed job", one row at a time. It reads the
// cluster through kubectl (read-only) and measures the volume through the storage probe; it never
// applies a manifest or changes a cluster object.
package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bolubo/checkpoint-governor/internal/storage"
)

// Status is the three-word vocabulary the check prints. "unknown" is always accompanied by a reason:
// a row that says only "unknown" is a row nobody can act on.
type Status string

const (
	OK      Status = "ok"
	Blocked Status = "blocked"
	Unknown Status = "unknown"
)

// Row is one check.
type Row struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

// Report is the whole check.
type Report struct {
	Schema  int    `json:"schema"`
	Dir     string `json:"dir"`
	Context string `json:"context,omitempty"`
	Queue   string `json:"queue,omitempty"`
	Rows    []Row  `json:"checks"`
	Ready   bool   `json:"ready"`
}

// Runner runs kubectl (or a fake, in tests). It returns only stdout; a non-zero exit is an error.
type Runner interface {
	Output(args ...string) ([]byte, error)
}

// Exec is the real kubectl.
type Exec struct {
	Kubectl string
	// Context is passed as --context to every call when it is set; an empty value means "whatever
	// kubectl currently selects", which is what a check on a laptop should do.
	Context string
}

func (e Exec) Output(args ...string) ([]byte, error) {
	bin := e.Kubectl
	if bin == "" {
		bin = "kubectl"
	}
	if e.Context != "" {
		args = append([]string{"--context", e.Context}, args...)
	}
	cmd := exec.Command(bin, args...)
	return cmd.Output()
}

// lookPath is a variable so a test can say what is installed without changing the machine.
var lookPath = exec.LookPath

// Check reads the cluster and the directory. It never fails as a whole: each row carries its own
// answer, and Ready is false when any row is blocked.
func Check(dir, kubeContext, queue string, runner Runner) Report {
	rep := Report{Schema: 1, Dir: dir, Context: kubeContext, Queue: queue}

	// kubectl and the selected context. `get namespace default` exercises the credentials; a config
	// read alone would say "selected" for a context the server rejects.
	if _, err := runner.Output("get", "namespace", "default", "-o", "name"); err != nil {
		rep.Rows = append(rep.Rows, Row{Name: "kubectl", Status: Blocked,
			Detail: "cannot reach the cluster: " + err.Error(),
			Remedy: "install kubectl and select a working context"})
	} else {
		detail := "the cluster answers"
		if kubeContext != "" {
			detail = "context " + kubeContext + " answers"
		}
		rep.Rows = append(rep.Rows, Row{Name: "kubectl", Status: OK, Detail: detail})
	}

	// Kueue: the queue exists and reports a CPU quota.
	if queue == "" {
		rep.Rows = append(rep.Rows, Row{Name: "kueue", Status: Blocked, Detail: "no --queue was given",
			Remedy: "pass --queue=<ClusterQueue>"})
	} else if out, err := runner.Output("get", "clusterqueue", queue, "-o",
		"jsonpath={.spec.resourceGroups[0].flavors[0].resources[0].nominalQuota}"); err != nil {
		rep.Rows = append(rep.Rows, Row{Name: "kueue", Status: Blocked,
			Detail: "ClusterQueue " + queue + " is not readable: " + err.Error(),
			Remedy: "install Kueue, or name an existing ClusterQueue"})
	} else {
		quota := strings.TrimSpace(string(out))
		rep.Rows = append(rep.Rows, Row{Name: "kueue", Status: OK,
			Detail: fmt.Sprintf("ClusterQueue %s, CPU quota %s", queue, quota)})
	}

	// Capacity: the nodes that can hold the pods.
	if out, err := runner.Output("get", "nodes", "-o", "jsonpath={.items[*].status.allocatable.cpu}"); err != nil {
		rep.Rows = append(rep.Rows, Row{Name: "capacity", Status: Blocked,
			Detail: "cannot read node allocatable: " + err.Error(),
			Remedy: "check the context's node permissions"})
	} else {
		cpus := strings.Fields(string(out))
		if len(cpus) == 0 {
			rep.Rows = append(rep.Rows, Row{Name: "capacity", Status: Blocked,
				Detail: "no node reports allocatable CPU", Remedy: "check `kubectl get nodes`"})
		} else {
			rep.Rows = append(rep.Rows, Row{Name: "capacity", Status: OK,
				Detail: fmt.Sprintf("%d node(s), CPU %s", len(cpus), strings.Join(cpus, " "))})
		}
	}

	// The volume, measured by the same probe doctor uses.
	if r, err := storage.Probe(dir); err != nil {
		rep.Rows = append(rep.Rows, Row{Name: "volume", Status: Blocked,
			Detail: "cannot measure " + dir + ": " + err.Error(),
			Remedy: "mount the checkpoint directory at the same path the job sees"})
	} else {
		rep.Rows = append(rep.Rows, Row{Name: "volume", Status: OK,
			Detail: fmt.Sprintf("%s (%s)", r.Class, r.FSName)})
		st := Row{Name: "storage", Status: OK, Detail: r.Promise}
		if !r.Safe() {
			st.Status = Blocked
			st.Remedy = "use a shared volume that passes `ckptctl doctor --storage`, or do not resume across pods"
		}
		rep.Rows = append(rep.Rows, st)
	}

	// The gate binary and the controller's published plan.
	if path, err := lookPath("ckptgate"); err != nil {
		rep.Rows = append(rep.Rows, Row{Name: "gate", Status: Blocked,
			Detail: "ckptgate is not on PATH", Remedy: "install it (install.sh), then re-run this check"})
	} else {
		rep.Rows = append(rep.Rows, Row{Name: "gate", Status: OK, Detail: path})
	}
	state := filepath.Join(dir, ".ckpt-governor", "state.json")
	if _, err := os.Stat(state); err != nil {
		rep.Rows = append(rep.Rows, Row{Name: "controller", Status: Unknown,
			Detail: "no plan in " + dir, Remedy: "run one governed job first (`ckptctl wrap` or the Python client)"})
	} else {
		rep.Rows = append(rep.Rows, Row{Name: "controller", Status: OK, Detail: "a plan is published"})
	}

	rep.Ready = true
	for _, row := range rep.Rows {
		if row.Status == Blocked {
			rep.Ready = false
		}
	}
	return rep
}

// WriteOptions is what the generated files need from the operator.
type WriteOptions struct {
	Dir       string
	Root      string
	Image     string
	Claim     string
	Queue     string
	WorldDir  string
	Cluster   string
	Namespace string
}

// Write emits the files a platform engineer applies or copies: a governed Job, the gate wrapper, the
// agent registration, and a three-command README. It writes no cluster object.
func Write(outDir string, opt WriteOptions) ([]string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	root := opt.Root
	if root == "" {
		root = opt.Dir
	}
	image := opt.Image
	if image == "" {
		image = "ckpt-governor:local"
	}
	claim := opt.Claim
	if claim == "" {
		claim = "ckpt-claim"
	}
	ns := opt.Namespace
	if ns == "" {
		ns = "default"
	}

	job := fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: governed-job
  namespace: %s
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: train
          image: %s
          command: ["ckptctl", "wrap", "--dir=%s", "--", "python", "-m", "train"]
          volumeMounts:
            - {name: ckpt, mountPath: %s}
      volumes:
        - name: ckpt
          persistentVolumeClaim: {claimName: %s}
`, ns, image, root, root, claim)

	gate := fmt.Sprintf(`#!/bin/sh
# Submit the arrival through the gate: the checkpoint directory, the world and the queue are the
# three numbers the hold is computed against.
WORLD=${WORLD:-%s}
CLUSTER=${CLUSTER:-%s}
exec ckptgate submit \
  --manifest=%s/governed-job.yaml \
  --world="$WORLD" --cluster="$CLUSTER" \
  --queue=%s --ckpt-root=%s --deadline=2m
`, opt.WorldDir, opt.Cluster, outDir, opt.Queue, root)

	agent := fmt.Sprintf(`{
  "mcpServers": {
    "ckptctl": {
      "type": "stdio",
      "command": "ckptctl",
      "args": ["mcp", "--root", %q]
    }
  }
}
`, root)

	readme := fmt.Sprintf(`# Governed job

1. Replace the image and the PVC name in `+"`governed-job.yaml`"+`.
2. Apply it: `+"`kubectl apply -f governed-job.yaml`"+`.
3. Ask what happened: `+"`ckptctl status %s`"+` and `+"`ckptctl evidence --dir=%s`"+`.
4. Register the agent: `+"`ckptctl setup --agent=claude --apply --root=%s`"+`.
`, root, root, root)

	files := []struct {
		name string
		data string
		mode os.FileMode
	}{
		{"governed-job.yaml", job, 0o644},
		{"submit.sh", gate, 0o755},
		{"agent-mcp.json", agent, 0o644},
		{"README.md", readme, 0o644},
	}
	var written []string
	for _, f := range files {
		path := filepath.Join(outDir, f.name)
		if err := os.WriteFile(path, []byte(f.data), f.mode); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}

// Marshal is the JSON view of a report, used by `setup --check --json`.
func (r Report) Marshal() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }
