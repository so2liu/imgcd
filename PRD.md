好的，我帮你整理一个简短、完整的产品文档，把背景、API 设计、示例都包含进去。

⸻

imgcd 产品文档

背景

在离线环境中部署和更新容器镜像时，通常需要通过光盘或 U 盘导入镜像。
传统方式是使用 docker save / docker load 导出和导入，但这会生成完整的大体积 tar 包，即使只是镜像更新了几个层，也要重复传输所有层。

imgcd 工具的目标：
	•	让镜像导出/导入 支持分层增量，只需传输新增层。
	•	保持 CLI 极简，只需要两条命令：save 和 load。
	•	输出结果是一个 .tar.gz 文件，包含所有必要的元信息（manifest/config/layers），导入时无需指定镜像名。

支持macOS和主流Linux系统
⸻

API 设计

1. 导出镜像

imgcd save <NEW_REF> [--since <OLD_REF>] [--out-dir <DIR>]

	•	参数
	•	<NEW_REF>：必填，新镜像的引用，例如 ns/app:1.2.9。
	•	--since <OLD_REF>：可选，与旧镜像对比，只导出差异层。若不指定，则导出全量。
	•	--out-dir <DIR>：可选，指定输出目录，默认是 ./out/。
	•	输出文件名
自动生成，格式：

<repo>_<name>-<NEW_TAG>__since-<OLD_TAG|none>.tar.gz



⸻

2. 导入镜像

imgcd load --from <FILE>

	•	参数
	•	--from <FILE>：必填，指定 .tar.gz 包路径。
	•	行为
	•	自动读取包内的 meta 信息，恢复镜像的 repository 和 tag。
	•	自动检测环境：优先导入到 Docker，若无 Docker 则尝试 containerd。
	•	已存在的层会跳过，只导入缺失的层。

⸻

示例

导出镜像

# 全量导出 ns/app:1.0.0
imgcd save ns/app:1.0.0
# => ./out/ns_app-1.0.0__since-none.tar.gz

# 增量导出 ns/app:1.2.9 相对于 ns/app:1.2.8
imgcd save ns/app:1.2.9 --since ns/app:1.2.8
# => ./out/ns_app-1.2.9__since-1.2.8.tar.gz

# 导出到指定目录
imgcd save ns/app:2.0.0 --since ns/app:1.9.0 --out-dir /tmp/bundles
# => /tmp/bundles/ns_app-2.0.0__since-1.9.0.tar.gz

导入镜像

# 从 tar.gz 导入（镜像名和 tag 自动识别）
imgcd load --from ./out/ns_app-1.2.9__since-1.2.8.tar.gz
# => 成功导入镜像 ns/app:1.2.9


⸻

总结
	•	imgcd save：生成镜像增量包（始终为 .tar.gz，自动命名）。
	•	imgcd load：从文件导入镜像（无需指定镜像名，自动识别）。
	•	简洁、增量、离线友好。

⸻

