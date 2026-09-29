# MHQ GM Console

React + TypeScript + Vite 管理前端。前端只通过同源 `/api` 调用 `server-mysql/cmd/gm-api`，
不会保存 MySQL DSN、Redis 密码或游戏服密钥。

## 本地开发

```powershell
npm install
npm run dev
```

默认打开 <http://127.0.0.1:5173/>。Vite 会把 `/api` 代理到 `http://127.0.0.1:8080`。

## 生产构建

```powershell
npm install --offline --no-audit --no-fund
npm run build
```

构建产物在 `dist/`，部署到 Nginx 静态目录，并将 `/api/` 反代到 GM API 回环端口。

## 中文物品选择

角色详情 → 发放物品：输入中文名称或 ID，选择物品分类、适用职业，再点击“添加”并填写数量。
职业筛选包含军官、运动员、护士、超能力及通用；选择职业时保留通用装备。
装备显示具体部位，并可按武器、衣服、称号、形象等 12 个部位进一步筛选。
同名装备按不同 ID 分别显示。需要离线查阅时，可点击“下载中文清单”。
物品名称和职业由 `/api/v1/items` 读取当前 MySQL 配置，前端不内置静态 ID 表。
“发送邮件”也使用同一选择器添加多种附件；不选附件时发送纯文字邮件，附件在游戏中领取后到账。
角色操作完成后保留当前功能窗口、筛选条件及填写内容，并在窗口内显示执行结果。
可以继续操作，只有主动关闭、取消或按 Esc 时退出窗口。

## 目录

- `src/api/`：API 类型、Cookie/CSRF、幂等请求封装；
- `src/layouts/`：控制台壳层、响应式侧栏和状态栏；
- `src/pages/`：登录、总览、角色、运营、配置、审计页面；
- `src/components/`：按钮、卡片、状态、表单和加载/错误状态；
- `src/styles/`：深色技术型设计系统样式；
- `design-system/`：`ui-ux-pro-max` 生成的视觉基线。
