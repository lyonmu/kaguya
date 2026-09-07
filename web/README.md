# Kaguya Agent Console

Kaguya 的 React 管理控制台。当前仅实现已有 API 支持的「系统管理 / 访问日志」页面。

## 技术栈

- React 19 + TypeScript + Vite
- Ant Design
- Tailwind CSS 4
- Day.js

页面支持明亮/暗黑模式，并会将用户选择保存在 `localStorage` 中；首次访问默认跟随操作系统。

## 本地开发

```bash
bun install
bun run dev
```

开发服务器会将 `/kaguya/api` 代理到 `http://localhost:9024`，请先启动 Kaguya 后端。

如 API 使用其他基础地址，可在构建或启动前指定：

```bash
VITE_API_BASE_URL=https://example.com/kaguya/api bun run build
```

## 构建与嵌入

在仓库根目录执行：

```bash
make build
```

该命令会构建前端，将 `web/dist/` 同步到 Go 的嵌入资源目录，再生成包含前端静态文件的单一 Kaguya 二进制。启动后访问 `http://localhost:9024/`。

单独校验前端：

```bash
bun run lint
bun run build
```

## 目录

- `src/api/`：通用 HTTP 客户端
- `src/components/`：共享布局组件
- `src/features/`：按业务领域组织的 API、类型与状态逻辑
- `src/pages/`：页面组件
- `src/app/`：应用级主题与模式配置
- `src/styles/`：Tailwind 入口及全局设计变量
