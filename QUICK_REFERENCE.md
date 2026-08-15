# RedstoneAPI 快速参考

**GitHub**: https://github.com/silent-QAQ/redstoneapi  
**最后更新**: 2026-08-16

---

## 📁 重要文档

| 文档 | 说明 |
|------|------|
| `PROJECT_STATUS.md` | 项目状态总览（模块完成度、文件统计） |
| `PROGRESS_UPDATE.md` | 开发进度更新（任务清单、已知问题） |
| `DAILY_SUMMARY_2026-08-16.md` | 今日工作总结 |
| `docs/redstone/DEVELOPMENT_ROADMAP.md` | 开发路线图（详细计划） |
| `docs/redstone/UPSTREAM_SYNC_STRATEGY.md` | 上游同步策略 |

---

## 🚀 当前任务

| 任务 | 优先级 | 预计工作量 | 状态 |
|------|--------|-----------|------|
| #1 完善账号验真功能 | 🔴 高 | 8-12 小时 | ⏳ 待开始 |
| #2 钱包前端页面 | 🔴 高 | 6-8 小时 | ⏳ 待开始 |
| #3 文档补充 | 🟡 中 | 4-6 小时 | ⏳ 待开始 |

---

## 📊 项目状态速览

```
总完成度:     55%
后端代码:     75%
前端代码:     40%
测试覆盖:     30%
文档:         50%
```

### 核心功能状态
```
账号验真: ████████░░ 85%
钱包系统: █████████░ 90%
交易行:   ████████░░ 80%
账号共享: ███████░░░ 75%
集群运维: ███████░░░ 70%
```

---

## 🎯 本周目标

- [ ] 完善账号验真功能（85% → 100%）
- [ ] 钱包前端页面上线
- [ ] 基础文档完善
- [ ] 项目完成度: 55% → 65%

---

## 🔧 快速命令

### Git 操作
```bash
# 查看状态
git status

# 拉取最新代码
git pull origin main

# 推送代码
git add .
git commit -m "feat: xxx"
git push origin main
```

### 后端操作
```bash
cd backend
make run           # 启动后端
make test          # 运行测试
make migrate       # 运行迁移
```

### 前端操作
```bash
cd frontend
npm run dev        # 启动开发服务器
npm run build      # 构建生产版本
npm run test       # 运行测试
```

---

## 📂 目录结构

```
redstoneapi/
├── backend/
│   ├── internal/
│   │   └── redstone/         # Redstone 核心模块
│   │       ├── controlledaccount/  # 账号管理 + 验真
│   │       ├── wallet/             # 钱包系统
│   │       ├── market/             # 交易行
│   │       ├── sharing/            # 账号共享
│   │       ├── cluster/            # 集群管理
│   │       └── operations/         # 运维管理
│   ├── migrations/           # 数据库迁移（247 个）
│   └── ...
├── frontend/
│   └── src/
│       ├── views/            # 页面组件
│       ├── components/       # 通用组件
│       └── api/              # API 调用
├── docs/
│   └── redstone/             # Redstone 文档
└── [项目文档]
```

---

## 🔑 关键文件

### 后端核心文件
```
backend/internal/redstone/controlledaccount/verifier.go   (700+ 行)
backend/internal/redstone/controlledaccount/scheduler.go  (360+ 行)
backend/internal/redstone/wallet/wallet.go                (33KB)
backend/internal/redstone/market/                         (20+ 文件)
backend/internal/redstone/sharing/                        (25+ 文件)
```

### 数据库迁移
```
backend/migrations/9400_redstone_account_verification.sql  # 账号验真表
backend/migrations/9000_redstone_foundation.sql            # Redstone 基础
backend/migrations/9100_redstone_sharing_foundation.sql    # 共享系统
```

---

## 💡 下一步行动

### 立即开始
1. **任务 #1**: 账号验真 API 端点
   - 文件: `backend/internal/redstone/controlledaccount/handler.go`
   - 添加 3-5 个 API 端点
   - 编写单元测试

### 本周完成
2. **任务 #2**: 钱包前端页面
3. **任务 #3**: 文档补充

---

## 📞 协作

- **Git 分支**: `main`
- **提交规范**: Conventional Commits (feat/fix/docs/...)
- **代码审查**: 提交前自测
- **测试要求**: 每个功能必须有测试

---

**快速访问**: 
- 项目状态: `PROJECT_STATUS.md`
- 进度更新: `PROGRESS_UPDATE.md`
- GitHub: https://github.com/silent-QAQ/redstoneapi
