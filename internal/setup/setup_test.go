package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	out map[string]string
	err map[string]error
}

func (f fakeRunner) Output(args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	if err, ok := f.err[key]; ok {
		return nil, err
	}
	if v, ok := f.out[key]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("unexpected kubectl call: " + key)
}

func readyRunner() fakeRunner {
	return fakeRunner{
		out: map[string]string{
			"get namespace default -o name": "namespace/default",
			"get clusterqueue lab -o jsonpath={.spec.resourceGroups[0].flavors[0].resources[0].nominalQuota}": "4",
			"get nodes -o jsonpath={.items[*].status.allocatable.cpu}":                                        "24",
		},
	}
}

func TestCheckReadyNeedsNoClusterObjects(t *testing.T) {
	old := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/ckptgate", nil }
	defer func() { lookPath = old }()

	dir := t.TempDir()
	rep := Check(dir, "kind-kueue-lab", "lab", readyRunner())
	if !rep.Ready {
		t.Fatalf("report is blocked: %+v", rep.Rows)
	}
	for _, row := range rep.Rows {
		if row.Detail == "" {
			t.Fatalf("row %s has no detail", row.Name)
		}
	}
}

func TestCheckAMissingQueueIsBlockedWithARemedy(t *testing.T) {
	dir := t.TempDir()
	runner := readyRunner()
	runner.err = map[string]error{
		"get clusterqueue lab -o jsonpath={.spec.resourceGroups[0].flavors[0].resources[0].nominalQuota}": errors.New("not found"),
	}
	rep := Check(dir, "kind-kueue-lab", "lab", runner)
	if rep.Ready {
		t.Fatal("a missing queue must block")
	}
	for _, row := range rep.Rows {
		if row.Name == "kueue" && (row.Status != Blocked || row.Remedy == "") {
			t.Fatalf("kueue row = %+v", row)
		}
	}
}

func TestCheckUnknownAlwaysHasAReason(t *testing.T) {
	dir := t.TempDir()
	runner := readyRunner()
	runner.err = map[string]error{
		"get nodes -o jsonpath={.items[*].status.allocatable.cpu}": errors.New("forbidden"),
	}
	rep := Check(dir, "ctx", "lab", runner)
	if rep.Ready {
		t.Fatal("an unreadable capacity must not claim ready")
	}
	for _, row := range rep.Rows {
		if row.Status == Unknown && row.Detail == "" {
			t.Fatalf("unknown row has no reason: %+v", row)
		}
	}
}

func TestWriteEmitsTheFourFiles(t *testing.T) {
	out := t.TempDir()
	files, err := Write(out, WriteOptions{Dir: "/ckpt/job", Queue: "lab", WorldDir: "/world", Cluster: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("files = %v", files)
	}
	job, err := os.ReadFile(filepath.Join(out, "governed-job.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ckptctl", "wrap", "/ckpt/job", "ckpt-claim"} {
		if !strings.Contains(string(job), want) {
			t.Fatalf("job is missing %q:\n%s", want, job)
		}
	}
	submit, err := os.ReadFile(filepath.Join(out, "submit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ckptgate submit", "--queue=lab", "--ckpt-root=/ckpt/job"} {
		if !strings.Contains(string(submit), want) {
			t.Fatalf("submit.sh is missing %q:\n%s", want, submit)
		}
	}
}
