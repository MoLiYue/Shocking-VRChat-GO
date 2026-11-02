---
name: git-push-github
description: 提交代码到 GitHub。当用户说"提交"、"push"、"commit"、"推上去"、"同步"时使用此 skill。本项目 (Shocking-VRChat-GO) 直接推 main 分支到 GitHub，不走 Gerrit。
---

# Git Push to GitHub

## Git 身份

本仓库必须使用以下身份提交（仓库级配置，不改全局）：

```bash
git config user.name "MoLiYue"
git config user.email "moliyue018@gmail.com"
```

每次 commit 前检查 `git config user.name` / `git config user.email`。

## 流程

1. 推送前检查：`gofmt -l .` 无输出、`go vet ./...`、`GOOS=windows go vet ./...`、`go test ./...`
2. 修改了前端时：`cd frontend && npm run build`（输出到 `assets/static/`，需要一起提交）
3. 修改了 `wave_presets/` 或 `dg-lab/` 时：`go run ./tools/packassets`（刷新 `assets/bundled.zip`）
4. `git add <具体文件>`（不要 `git add .`）
5. `git commit -m "<type>: <description>"`（英文 conventional commits，标题 ≤ 70 字符，无 Change-Id / GEN tag）
6. `git push`

## 禁止

- `git push --force`、`git reset --hard`、`git add .`
- 未经用户明确要求（"发布"/"打 tag"/"release"）不要推 tag

## Release

推送 `v*` tag 会触发 `.github/workflows/build-go.yml`，构建 `shocking_vrchat_go_windows_x64.zip`
与 `sha256sums.txt` 并发布到 GitHub Release（应用内更新器按此文件名查找）。

```bash
git tag -a v0.x.x -m "v0.x.x: description"
git push origin v0.x.x
```
