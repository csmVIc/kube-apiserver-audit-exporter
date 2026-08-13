package exporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
)

func TestDefaultModeReadsExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writeAuditEvents(t, path, false, auditEvent("existing"))

	e := NewExporter(WithFile(path))
	t.Cleanup(func() { _ = e.closeAuditFile() })
	if err := e.processFileUpdate(path); err != nil {
		t.Fatal(err)
	}

	if _, ok := e.podCreationTimes[target{Name: "existing", Namespace: "test"}]; !ok {
		t.Fatal("existing audit event was not processed")
	}
}

func TestStartAtEndSkipsExistingAndReadsAppendedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writeAuditEvents(t, path, false, auditEvent("existing"))

	e := NewExporter(WithFile(path), WithStartAtEnd(true))
	t.Cleanup(func() { _ = e.closeAuditFile() })
	if err := e.openAuditFile(path, true); err != nil {
		t.Fatal(err)
	}
	if err := e.processFileUpdate(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.podCreationTimes[target{Name: "existing", Namespace: "test"}]; ok {
		t.Fatal("existing audit event was processed")
	}

	writeAuditEvents(t, path, true, auditEvent("appended"))
	if err := e.processFileUpdate(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.podCreationTimes[target{Name: "appended", Namespace: "test"}]; !ok {
		t.Fatal("appended audit event was not processed")
	}
}

func TestRotationDrainsOldFileAndReadsNewFileOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	rotatedPath := filepath.Join(dir, "audit.log.1")
	writeAuditEvents(t, path, false, auditEvent("initial"))

	e := NewExporter(WithFile(path))
	t.Cleanup(func() { _ = e.closeAuditFile() })
	if err := e.processFileUpdate(path); err != nil {
		t.Fatal(err)
	}

	writeAuditEvents(t, path, true, auditEvent("old-tail"))
	if err := os.Rename(path, rotatedPath); err != nil {
		t.Fatal(err)
	}
	writeAuditEvents(t, path, false, auditEvent("new-head"))

	if err := e.processFileUpdate(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old-tail", "new-head"} {
		if _, ok := e.podCreationTimes[target{Name: name, Namespace: "test"}]; !ok {
			t.Fatalf("audit event %q was not processed", name)
		}
	}

	newInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	openInfo, err := e.openFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(openInfo, newInfo) {
		t.Fatal("exporter did not switch to the new audit log")
	}

	stateSize := len(e.podCreationTimes)
	if err := e.processFileUpdate(path); err != nil {
		t.Fatal(err)
	}
	if len(e.podCreationTimes) != stateSize {
		t.Fatal("new audit log was processed more than once")
	}
}

func auditEvent(name string) auditv1.Event {
	pod, _ := json.Marshal(Pod{
		Metadata: Metadata{Name: name, Namespace: "test"},
	})
	return auditv1.Event{
		Verb:           "create",
		Stage:          auditv1.StageResponseComplete,
		StageTimestamp: metav1.MicroTime{Time: time.Now()},
		UserAgent:      "kube-controller-manager/v1",
		ObjectRef: &auditv1.ObjectReference{
			Resource:  "pods",
			Namespace: "test",
			Name:      name,
		},
		ResponseStatus: &metav1.Status{Code: 201},
		ResponseObject: &runtime.Unknown{Raw: pod},
	}
}

func writeAuditEvents(t *testing.T, path string, appendFile bool, events ...auditv1.Event) {
	t.Helper()
	flags := os.O_CREATE | os.O_WRONLY
	if appendFile {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}
