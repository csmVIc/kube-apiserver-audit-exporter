# kube-apiserver-audit-exporter
Designed to export kube-apiserver audit logs as metrics indicators.

The `yunikorn_workload_pods_scheduled_total{cluster,namespace}` counter tracks only YuniKorn workload Pods created by `kube-controller-manager` and successfully bound. YuniKorn placeholder Pods are excluded.
