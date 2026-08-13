# kube-apiserver-audit-exporter
Designed to export kube-apiserver audit logs as metrics indicators.

The `yunikorn_workload_pods_scheduled_total{cluster,namespace}` counter tracks only YuniKorn workload Pods created by `kube-controller-manager` and successfully bound. YuniKorn placeholder Pods are excluded.

Use `--start-at-end` to skip audit events that already exist when the exporter starts. While running, the exporter follows kube-apiserver log rotation by finishing the old open file before reading the new audit log from the beginning.
