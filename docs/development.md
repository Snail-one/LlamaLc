# 开发、测试与发布

要求 Go 1.22 或更高版本，不使用第三方 Go 模块。

```sh
go test ./...
go test -race ./...
go vet ./...
./scripts/build.sh
./scripts/build-windows-from-linux.sh "$(git describe --tags --exact-match)" amd64
```

`scripts/build.sh` 接受 `GOOS`、`GOARCH`、`VERSION`、`COMMIT` 和 `BUILD_DATE`。未指定 `VERSION` 时，仅在当前提交正好有 Git tag 时使用该 tag，否则版本为 `dev`。发布工作流先在 Linux 和 Windows 原生 runner 上执行测试、竞态检测与 vet，再构建 Linux/Windows 的 amd64/arm64 四个目标。归档名为 `llamalc-<os>-<arch>-<version>`，版本位于名称末尾，并发布 `SHA256SUMS.txt`。

Windows 清单位于 `build/windows/manifest.json`，两个程序均以当前用户权限运行。Windows 测试覆盖 DACL、写穿透文件替换、自动重启和就绪事件，而不是只做交叉编译。
