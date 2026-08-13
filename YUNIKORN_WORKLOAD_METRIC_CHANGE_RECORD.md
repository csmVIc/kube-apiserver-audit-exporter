> 如非必要引用源码，尽量使用自然语言解释本文涉及的实现与行为。

# Audit Exporter 源码修改记录

## 1. 修改目的

YuniKorn 的 Gang 调度会创建 placeholder Pod。原有审计指标无法区分实际工作 Pod 与 placeholder Pod，直接统计 Pod binding 会把两者一起计入；通过两个累计指标相减又可能产生曲线下降，因此无法稳定表示实际工作 Pod 的调度进度。

本次修改在 Audit Exporter 内关联 Pod 创建事件和 binding 事件，直接生成只统计 YuniKorn 实际工作 Pod 的单调递增指标。

## 2. 新增指标

新增 Prometheus Counter：

```text
yunikorn_workload_pods_scheduled_total{cluster,namespace}
```

它表示由 Kubernetes Controller Manager 创建、随后成功完成 binding 的 YuniKorn 实际工作 Pod 总数。

该指标仅在 `cluster=yunikorn` 时计算，不改变 Kueue、Volcano 或现有指标的统计行为。被 Prometheus 抓取后，目标资源命名空间仍按当前 ServiceMonitor 配置保存为 `exported_namespace`。

## 3. 事件关联方式

Exporter 按命名空间和 Pod 名称关联以下两类成功审计事件：

1. Controller Manager 创建 Pod：确认该 Pod 是实际工作 Pod，并记录其身份。
2. YuniKorn 创建 Pod binding：仅当该 Pod 已被识别为实际工作 Pod 时，指标才增加一次。

由 YuniKorn 自身创建的 placeholder Pod 会被排除，不参与新增指标。实现同时兼容 binding 事件先于 Pod 创建事件到达的情况，并忽略重复 binding，避免重复计数。Pod 删除后会清理对应的临时关联状态。

## 4. 对现有行为的影响

- 保留原有 API 请求、Pod 调度延迟、删除、完成和 Job 完成延迟指标。
- 不修改审计策略，也不要求 YuniKorn 为 placeholder Pod 增加额外标签。
- 不通过累计指标相减推导工作 Pod 数量，因此新曲线保持单调递增。
- Exporter 重启后会按照其既有审计文件处理方式重新建立内存状态和指标。

## 5. 测试覆盖

新增单元测试覆盖：

- 正常工作 Pod 创建后完成 binding，计数增加一次。
- placeholder Pod 不计数。
- binding 先于 Pod 创建事件到达时仍能正确计数。
- 重复 binding 不重复计数。
- 非 YuniKorn 集群不产生该指标计数。
- Pod 创建事件对象引用缺少名称时，能够从响应对象获得生成后的真实名称。

本地已通过全部 Go 单元测试和静态检查。服务器使用一个由 Job Controller 创建并由 YuniKorn 绑定的 Pod 完成冒烟验证，`bench-yunikorn` 的新指标从 `10001` 增至 `10002`，Prometheus 也成功采集对应样本。

## 6. 构建与发布

仓库新增最小化容器构建文件，并调整 GitHub Actions：发布版本标签时先执行测试，再构建 `linux/amd64` 镜像并推送到 GHCR。

本次修复版本为：

```text
ghcr.io/csmvic/kube-apiserver-audit-exporter:v0.0.27
```

镜像索引摘要：

```text
sha256:10b168ab841e2c9353ab99aa6056d221fe84bf8bc1aa7e8798a3195190c10ed7
```

主要源码提交：

- `6638871`：新增 YuniKorn 实际工作 Pod 调度计数及测试。
- `513b2e6`：兼容创建事件对象引用中 Pod 名称为空的实际审计格式。

## 7. 涉及文件

- `exporter/metrics.go`：指标定义、Pod 状态关联和计数逻辑。
- `exporter/exporter.go`：Exporter 内部状态初始化。
- `exporter/yunikorn_metrics_test.go`：新增行为测试。
- `README.md`：新增指标说明。
- `Dockerfile`、`.dockerignore`：容器镜像构建。
- `.github/workflows/go-cross-build.yml`：测试和 GHCR 发布流程。

## 8. 审计文件轮询精度与日志调整

Exporter 的审计文件轮询间隔由 `1s` 调整为 `100ms`，与 Prometheus 对该 Exporter 的 `100ms` 抓取间隔保持一致。每轮处理结束后仍从当前时刻重新计算下一次轮询，延续原有串行处理方式，避免文件处理较慢时连续追赶轮询。

空闲时的“没有新增日志”和常规“文件处理完成”日志降为 Debug；默认日志级别下不再持续输出这些信息，处理异常仍使用 Error。启动阶段等待审计文件可用的检查间隔保持 `1s`。

本轮源码提交为 `4a4812f`，公开镜像为：

```text
ghcr.io/csmvic/kube-apiserver-audit-exporter:v0.0.28
```

镜像摘要：

```text
sha256:35af4b689d1dabb09490ab7f5f49b55af16a4be23a068cfcb2c7bca32eb84df9
```

## 9. 从文件末尾启动与日志轮转

新增 `--start-at-end` 参数。启用后，Exporter 将启动时的主审计文件末尾作为初始读取位置，只统计随后追加的事件；默认值为 `false`，未启用时仍从文件开头读取。

Exporter 启动时打开一次主路径 `audit.log` 并持续持有文件句柄。每轮轮询通过文件身份判断 kube-apiserver 是否已轮转日志：未轮转时继续读取当前文件；发生轮转时先读完旧文件尾部，再切换到新的 `audit.log` 并从头读取。Exporter 不扫描历史备份文件。

删除原先依赖“当前文件大小小于 offset”重置读取位置的分支。轮询间隔、日志级别、现有指标和 HTTP 服务行为保持不变。

本轮发布版本为：

```text
ghcr.io/csmvic/kube-apiserver-audit-exporter:v0.0.29
```
