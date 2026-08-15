# RedstoneAPI

**基于 sub2api 的增强型 AI 网关系统**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.26%2B-blue.svg)](https://golang.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-15%2B-blue.svg)](https://www.postgresql.org/)

## 📖 项目简介

RedstoneAPI 是基于 [sub2api](https://github.com/Wei-Shaw/sub2api) v0.1.175 的维护型 fork，在保持上游兼容性的前提下，增加了以下核心功能：

- 🔐 **账号自动验真系统** - 基于多协议的智能账号验证
- 💰 **双钱包系统** - 普通余额 + 绑定余额分离管理
- 🏪 **用户交易市场** - 加密托管的 P2P 交易平台
- 🤝 **账号共享服务** - 基于房间的账号租赁系统
- 📊 **集群运维功能** - 分布式部署与监控支持

## ⚠️ 核心设计原则

**上游可同步性原则** - RedstoneAPI 严格遵循模块化隔离设计：

- ✅ 所有代码位于 `backend/internal/redstone/` 命名空间
- ✅ 所有数据库表使用 `redstone_` 前缀
- ✅ 迁移文件使用 9000-9999 编号范围
- ✅ 仅通过接口扩展，不修改 sub2api 核心代码
- ✅ 支持随时 rebase 上游更新

详见：[上游同步策略文档](./docs/redstone/UPSTREAM_SYNC_STRATEGY.md)

## 🚀 快速开始

### 环境要求

- Go 1.26+
- PostgreSQL 15+
- Redis 6+
- Node.js 18+ (前端开发)

### 安装步骤

```bash
# 1. 克隆仓库
git clone https://github.com/silent-QAQ/redstoneapi.git
cd redstoneapi

# 2. 初始化后端
cd backend
cp config.example.yaml config.yaml
# 编辑 config.yaml 配置数据库连接

# 3. 运行数据库迁移
go run cmd/migrate/main.go up

# 4. 启动后端服务
go run cmd/server/main.go

# 5. 初始化前端（另开终端）
cd ../frontend
npm install
npm run dev
```

## 📁 项目结构

```
redstoneapi/
├── backend/
│   ├── cmd/                    # 命令行入口
│   ├── internal/
│   │   ├── redstone/          # 🔴 Redstone 核心模块
│   │   │   ├── wallet/        # 钱包系统
│   │   │   ├── market/        # 交易市场
│   │   │   ├── sharing/       # 账号共享
│   │   │   ├── controlledaccount/  # 账号验真
│   │   │   └── cluster/       # 集群管理
│   │   ├── handler/           # sub2api 核心（保持不变）
│   │   └── ...
│   ├── migrations/
│   │   ├── 001_*.sql         # sub2api 原始迁移
│   │   └── 9000_*.sql        # 🔴 Redstone 迁移
│   └── ...
├── frontend/                  # Vue 3 前端
├── docs/
│   └── redstone/             # 🔴 Redstone 文档
└── README.md
```

## 🎯 功能模块

### 1. 账号自动验真

- 支持 Anthropic/OpenAI/Google 等多协议
- 定时自动验证账号有效性
- 连续失败自动冻结机制
- 详细的验真历史记录

### 2. 双钱包系统

- **普通余额**：可充值、提现、交易
- **绑定余额**：仅限 API 消费，不可提现
- 不可变账本设计，完整审计追踪

### 3. 用户交易市场

- 加密托管内容交付
- 自动安全扫描（ClamAV）
- 24小时申诉期保护
- 智能卖家额度管理

### 4. 账号共享服务

- 灵活的房间模式（私有/公共/广场）
- 租约生命周期管理
- 自动结算与分账
- 排队与预约系统

## 📊 开发进度

**当前完成度**: 约 15%

- ✅ 项目骨架搭建
- ✅ 数据库迁移文件
- ✅ 账号验真功能（85%）
- ⏳ 钱包系统（开发中）
- ⏳ 交易市场（规划中）
- ⏳ 账号共享（规划中）

详见：[开发路线图](./docs/redstone/DEVELOPMENT_ROADMAP.md)

## 🤝 贡献指南

我们欢迎贡献！在提交 PR 前，请确保：

1. 遵循[上游同步策略](./docs/redstone/UPSTREAM_SYNC_STRATEGY.md)
2. 所有新功能位于 `redstone/` 命名空间
3. 数据库表使用 `redstone_` 前缀
4. 迁移文件使用 9000-9999 编号
5. 包含完整的单元测试

## 📄 许可证

本项目采用 MIT 许可证。详见 [LICENSE](./LICENSE) 文件。

## 🙏 致谢

- [sub2api](https://github.com/Wei-Shaw/sub2api) - 上游项目
- [veridrop](https://github.com/canarybyte/veridrop) - 验真逻辑参考

## 📞 联系方式

- GitHub Issues: https://github.com/silent-QAQ/redstoneapi/issues
- 上游项目: https://github.com/Wei-Shaw/sub2api

---

**注意**: 本项目处于早期开发阶段，不建议用于生产环境。
