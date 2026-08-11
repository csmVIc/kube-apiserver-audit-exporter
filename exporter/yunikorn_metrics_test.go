package exporter

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
)

func successfulEvent(name, namespace, userAgent, verb, subresource string) auditv1.Event {
	return auditv1.Event{
		Stage:          auditv1.StageResponseComplete,
		Verb:           verb,
		UserAgent:      userAgent,
		ObjectRef:      &auditv1.ObjectReference{Resource: "pods", Subresource: subresource, Namespace: namespace, Name: name},
		ResponseStatus: &metav1.Status{Code: 201},
		StageTimestamp: metav1.NewMicroTime(time.Now()),
	}
}

func podCreateEvent(t *testing.T, name, namespace, userAgent string) auditv1.Event {
	t.Helper()
	event := successfulEvent(name, namespace, userAgent, "create", "")
	raw, err := json.Marshal(Pod{Metadata: Metadata{Name: name, Namespace: namespace}})
	if err != nil {
		t.Fatal(err)
	}
	event.ResponseObject = &runtime.Unknown{Raw: raw}
	return event
}

func scheduledValue(t *testing.T, cluster, namespace string) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := yunikornWorkloadPodsScheduled.WithLabelValues(cluster, namespace).Write(metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}

func uniqueNamespace(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano())
}

func TestYuniKornWorkloadPodScheduled(t *testing.T) {
	namespace := uniqueNamespace(t)
	exporter := NewExporter()

	exporter.updateMetrics(yunikornClusterLabel, podCreateEvent(t, "workload", namespace, "kube-controller-manager/v1.32.2"))
	exporter.updateMetrics(yunikornClusterLabel, successfulEvent("workload", namespace, "yunikorn-scheduler/v1.6.0", "create", "binding"))

	if got := scheduledValue(t, yunikornClusterLabel, namespace); got != 1 {
		t.Fatalf("scheduled workload pods = %v, want 1", got)
	}
}

func TestYuniKornPlaceholderPodExcluded(t *testing.T) {
	namespace := uniqueNamespace(t)
	exporter := NewExporter()

	exporter.updateMetrics(yunikornClusterLabel, podCreateEvent(t, "placeholder", namespace, "yunikorn-scheduler/v1.6.0"))
	exporter.updateMetrics(yunikornClusterLabel, successfulEvent("placeholder", namespace, "yunikorn-scheduler/v1.6.0", "create", "binding"))

	if got := scheduledValue(t, yunikornClusterLabel, namespace); got != 0 {
		t.Fatalf("scheduled workload pods = %v, want 0", got)
	}
}

func TestYuniKornBindingBeforePodCreate(t *testing.T) {
	namespace := uniqueNamespace(t)
	exporter := NewExporter()

	exporter.updateMetrics(yunikornClusterLabel, successfulEvent("workload", namespace, "yunikorn-scheduler/v1.6.0", "create", "binding"))
	exporter.updateMetrics(yunikornClusterLabel, podCreateEvent(t, "workload", namespace, "kube-controller-manager/v1.32.2"))

	if got := scheduledValue(t, yunikornClusterLabel, namespace); got != 1 {
		t.Fatalf("scheduled workload pods = %v, want 1", got)
	}
}

func TestYuniKornDuplicateBindingNotCounted(t *testing.T) {
	namespace := uniqueNamespace(t)
	exporter := NewExporter()

	exporter.updateMetrics(yunikornClusterLabel, podCreateEvent(t, "workload", namespace, "kube-controller-manager/v1.32.2"))
	binding := successfulEvent("workload", namespace, "yunikorn-scheduler/v1.6.0", "create", "binding")
	exporter.updateMetrics(yunikornClusterLabel, binding)
	exporter.updateMetrics(yunikornClusterLabel, binding)

	if got := scheduledValue(t, yunikornClusterLabel, namespace); got != 1 {
		t.Fatalf("scheduled workload pods = %v, want 1", got)
	}
}

func TestOtherClusterDoesNotPublishYuniKornMetric(t *testing.T) {
	namespace := uniqueNamespace(t)
	exporter := NewExporter()

	exporter.updateMetrics("kueue", podCreateEvent(t, "workload", namespace, "kube-controller-manager/v1.32.2"))
	exporter.updateMetrics("kueue", successfulEvent("workload", namespace, "kube-scheduler/v1.32.2", "create", "binding"))

	if got := scheduledValue(t, "kueue", namespace); got != 0 {
		t.Fatalf("scheduled workload pods = %v, want 0", got)
	}
}
