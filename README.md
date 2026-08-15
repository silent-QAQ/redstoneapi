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

**当前完成度**: 约 55% (代码可交付度)

### 核心模块状态

| 模块 | 后端 | 前端 | 测试 | 总体完成度 |
|------|------|------|------|-----------|
| 🔐 账号验真 | 85% | 30% | 40% | **85%** |
| 💰 钱包系统 | 95% | 20% | 50% | **90%** |
| 🏪 交易市场 | 90% | 40% | 30% | **80%** |
| 🤝 账号共享 | 85% | 30% | 30% | **75%** |
| 📊 集群运维 | 80% | 20% | 40% | **70%** |

### 已完成功能 ✅

- ✅ 项目骨架与 Git 仓库搭建
- ✅ 完整的数据库迁移文件（247 个，含 27 个 Redstone）
- ✅ 账号验真核心逻辑（700+ 行，支持 Anthropic/OpenAI/Gemini）
- ✅ 自动调度器（360+ 行，支持分布式锁、连续失败自动冻结）
- ✅ 钱包系统核心服务（双余额、事务化、幂等性保证）
- ✅ 交易市场后端逻辑（商品、订单、加密托管、结算）
- ✅ 账号共享后端服务（房间、租约、预约、评价）
- ✅ 集群管理基础功能（节点心跳、任务租约）

### 待完成功能 ⏳

- ⏳ 账号验真 API 端点和前端界面（剩余 15%）
- ⏳ 钱包前端页面（余额展示、流水、充值提现）
- ⏳ 交易市场前端页面（商品浏览、发布、订单管理）
- ⏳ 账号共享前端页面（房间管理、租约、评价）
- ⏳ 管理后台治理页面（申诉、举报、审核）
- ⏳ 完整的单元测试和集成测试覆盖
- ⏳ API 文档和部署指南

### 近期任务

查看详细计划：[开发路线图](./docs/redstone/DEVELOPMENT_ROADMAP.md) | [快速参考](./QUICK_REFERENCE.md)

## 🤝 贡献指南

我们欢迎贡献！在提交 PR 前，请确保：

1. 遵循[上游同步策略](./docs/redstone/UPSTREAM_SYNC_STRATEGY.md)
2. 所有新功能位于 `redstone/` 命名空间
3. 数据库表使用 `redstone_` 前缀
4. 迁移文件使用 9000-9999 编号
5. 包含完整的单元测试

## 📄 许可证

本项目采用 MIT 许可证。详见 [LICENSE](./LICENSE) 文件。

## 📚 文档

- [快速参考手册](./QUICK_REFERENCE.md) - 快速上手和常用命令
- [项目状态总览](./PROJECT_STATUS.md) - 模块完成度和文件统计
- [开发进度更新](./PROGRESS_UPDATE.md) - 任务清单和已知问题
- [开发路线图](./docs/redstone/DEVELOPMENT_ROADMAP.md) - 详细开发计划
- [上游同步策略](./docs/redstone/UPSTREAM_SYNC_STRATEGY.md) - 模块化隔离规范

## 🙏 致谢

- [sub2api](https://github.com/Wei-Shaw/sub2api) - 上游项目
- [veridrop](https://github.com/canarybyte/veridrop) - 验真逻辑参考

## 📞 联系方式

- GitHub Issues: https://github.com/silent-QAQ/redstoneapi/issues
- 上游项目: https://github.com/Wei-Shaw/sub2api

---

**项目状态**: 积极开发中 | **代码完成度**: 约 55% | **后端核心功能**: 基本完成 | **前端界面**: 开发中
