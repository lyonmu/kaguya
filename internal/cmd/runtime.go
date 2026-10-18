package cmd

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	pkgid "github.com/lyonmu/gopkg/id"
	"github.com/lyonmu/kaguya/internal/agent/instructions"
	agentmcp "github.com/lyonmu/kaguya/internal/agent/mcp"
	systemapi "github.com/lyonmu/kaguya/internal/api/v1/system"
	"github.com/lyonmu/kaguya/internal/db"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	dtosystem "github.com/lyonmu/kaguya/internal/dto/system"
	_ "github.com/lyonmu/kaguya/internal/ent/runtime"
	"github.com/lyonmu/kaguya/internal/features"
	"github.com/lyonmu/kaguya/internal/global"
	initialize "github.com/lyonmu/kaguya/internal/init"
	"github.com/lyonmu/kaguya/internal/router"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
	memorysvc "github.com/lyonmu/kaguya/internal/service/memory"
	servicesystem "github.com/lyonmu/kaguya/internal/service/system"
	"github.com/lyonmu/kaguya/pkg"
	"go.uber.org/zap"
)

// initStageTimeout 限制数据库初始数据与提供商密钥校验阶段的耗时。
const initStageTimeout = 30 * time.Second

// appRuntime 持有 Web 与 Desktop 两种启动模式共享的初始化结果，并提供唯一的
// 释放入口。一个进程最多创建一个实例，close 是幂等的。
//
// 关闭顺序：先 beginShutdown 停止准入、取消 root context，等待在途 Handler 与
// 生成任务结束后再 close；close 负责 MCP、数据库与日志。
type appRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc

	systemSvc      *servicesystem.SystemSvc
	logger         *zap.Logger
	dbReady        bool
	instructions   *instructions.Manager
	titleQueue     *serviceagent.TitleQueue
	titlePublisher *serviceagent.TitlePublisher

	gate          *pkg.Admission
	restoreDone   chan struct{}
	restoreUp     bool
	recoveryDone  chan struct{}
	recoveryUp    bool
	modelSyncDone chan struct{}
	modelSyncUp   bool
	memoryDone    chan struct{}
	memoryUp      bool

	closeOnce sync.Once
}

func newAppRuntime(parent context.Context) *appRuntime {
	ctx, cancel := context.WithCancel(parent)
	return &appRuntime{
		ctx:           ctx,
		systemSvc:     servicesystem.NewSystemSvc(ctx),
		cancel:        cancel,
		gate:          pkg.NewAdmission(),
		restoreDone:   make(chan struct{}),
		recoveryDone:  make(chan struct{}),
		modelSyncDone: make(chan struct{}),
		memoryDone:    make(chan struct{}),
	}
}

// RootContext 返回请求生命周期 context；取消它会终止全部已登记的请求。
func (rt *appRuntime) RootContext() context.Context { return rt.ctx }

// Admission 返回请求准入门，供 Desktop 原生通道复用。
func (rt *appRuntime) Admission() *pkg.Admission { return rt.gate }

// init 按固定顺序完成日志、密钥、ID、数据库、初始数据与提供商密钥初始化。
// 任一步失败都返回错误；已取得的资源由调用方通过 close 释放。
func (rt *appRuntime) init() error {
	logger, err := global.Cfg.LogInfo.NewLogger()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}
	rt.logger = logger
	global.Logger = logger
	logger.Info("application is starting...")

	if err := rt.ctx.Err(); err != nil {
		return err
	}
	if err := initialize.SQLCipherKey(&global.Cfg.DB); err != nil {
		return rt.fail(fmt.Errorf("initialize SQLCipher key: %w", err))
	}

	// 创建 ID 生成器，传入机器 ID 获取函数
	gen, err := pkgid.NewSonySnowFlake(func() (int, error) {
		return global.Cfg.MachineID, nil // 每台机器应使用不同的 ID
	})
	if err != nil {
		return rt.fail(fmt.Errorf("create ID generator: %w", err))
	}
	global.Id = gen

	global.Logger.Info("start init database connection")
	if err := global.Cfg.DB.EnsureSQLiteDatabase(); err != nil {
		return rt.fail(fmt.Errorf("ensure sqlite database: %w", err))
	}
	entcli, err := db.InitSQLite(&global.Cfg.DB)
	if err != nil {
		return rt.fail(fmt.Errorf("init sqlite connection: %w", err))
	}
	db.EntClient = entcli
	rt.dbReady = true

	if err := rt.ctx.Err(); err != nil {
		return err
	}
	global.Logger.Info("start init application data")
	initCtx, cancelInit := context.WithTimeout(rt.ctx, initStageTimeout)
	initErr := initialize.Run(initCtx, db.EntClient)
	if initErr == nil && features.Memory {
		// 记忆搜索投影的 normalizer 升级需要从页面重新生成，小库启动时同步迁移；
		// FTS 结构迁移已在 db.InitSQLite 内完成。
		initErr = memorysvc.EnsureSearchProjection(initCtx, db.EntClient)
	}
	if initErr == nil {
		// 上次进程崩溃/强杀会留下 running 占位轮次：本实例启动时不接管它们。
		initErr = serviceagent.ReconcileRunningTurns(initCtx)
	}
	if initErr == nil {
		// 遗留的标题任务还原为 pending，排队等待本实例的 worker 继续执行。
		initErr = serviceagent.ReconcileTitleJobs(initCtx)
	}
	if initErr == nil {
		// 崩溃遗留的审批不可重放：pending/未消费 approved 一律终结为 canceled。
		initErr = serviceagent.ReconcileApprovals(initCtx)
	}
	cancelInit()
	if initErr != nil {
		return rt.fail(fmt.Errorf("initialize application data: %w", initErr))
	}

	// API Key 静态加密必须在读取提供商配置前就绪；初始化失败（旧格式未迁移、
	// 密钥材料不匹配）直接中止启动，避免用不可用密钥继续处理请求。
	dbKey, err := global.Cfg.DB.SQLCipherKeyBytes()
	if err != nil {
		return rt.fail(fmt.Errorf("read SQLCipher key for provider secret encryption: %w", err))
	}
	secretCtx, cancelSecret := context.WithTimeout(rt.ctx, initStageTimeout)
	secretErr := rt.systemSvc.InitSecret(secretCtx, global.Cfg.SecretKey, dbKey)
	cancelSecret()
	if secretErr != nil {
		return rt.fail(fmt.Errorf("initialize provider API key encryption: %w", secretErr))
	}

	// 全局指令 Manager：启动时同步加载，缺失文件允许为空；非法内容等严重错误中止启动。
	systemInfo, err := rt.systemSvc.Info(rt.ctx)
	if err != nil {
		return rt.fail(fmt.Errorf("load system info for global instructions: %w", err))
	}
	instructionManager, err := instructions.New(rt.ctx, systemInfo.GlobalAgentsPaths, instructions.Options{Logger: global.Logger})
	if err != nil {
		return rt.fail(fmt.Errorf("load global instructions: %w", err))
	}
	rt.instructions = instructionManager
	instructionManager.Start()
	serviceagent.DefaultGlobalInstructions = instructionManager
	servicesystem.ReloadGlobalInstructions = instructionManager.Reconfigure
	servicesystem.GlobalInstructionStatus = func() *dtosystem.InstructionStatus {
		status := instructionManager.Status()
		resp := &dtosystem.InstructionStatus{
			Version: status.Version, Hash: status.Hash, Generation: status.Generation, LoadedGeneration: status.LoadedGeneration, LastError: status.LastError,
		}
		if !status.LoadedAt.IsZero() {
			loadedAt := status.LoadedAt
			resp.LoadedAt = &loadedAt
		}
		return resp
	}

	// 标题事件发布者与 FIFO 队列随实例创建：订阅者与 worker 都受本进程生命周期约束。
	rt.titlePublisher = serviceagent.NewTitlePublisher()
	rt.titleQueue = serviceagent.NewTitleQueue(rt.ctx, db.EntClient, global.Logger)
	serviceagent.DefaultTitlePublisher = rt.titlePublisher
	serviceagent.DefaultTitleQueue = rt.titleQueue
	rt.titleQueue.Start()

	systemapi.UseRuntimeService(rt.systemSvc)
	rt.startMCPRestore()
	rt.startMCPRecovery()
	rt.startModelCatalogSync()
	rt.startMemoryWorker()
	return nil
}

// startMemoryWorker 启动长期记忆后台编译 Worker。Worker 使用 root context：
// beginShutdown 停止新领取并取消当前远程调用，close 等待其退出后再关闭数据库。
func (rt *appRuntime) startMemoryWorker() {
	if !features.Memory {
		return
	} // 暂停自动整理，保留 Worker 实现。
	rt.memoryUp = true
	worker := memorysvc.NewWorker(memorysvc.NewService(db.EntClient, global.Logger))
	go func() {
		defer close(rt.memoryDone)
		worker.Run(rt.ctx)
	}()
}

// startModelCatalogSync 启动 models.dev 目录调度器；手动同步与它共享互斥控制。
func (rt *appRuntime) startModelCatalogSync() {
	rt.modelSyncUp = true
	go func() {
		defer close(rt.modelSyncDone)
		servicesystem.DefaultModelCatalogSyncer.Run(rt.ctx)
	}()
}

// startMCPRestore 异步恢复 MCP 连接；连接生命周期独立于单次请求，必须由
// close 等待并统一释放。
func (rt *appRuntime) startMCPRestore() {
	rt.restoreUp = true
	go func() {
		defer close(rt.restoreDone)
		if err := rt.systemSvc.RestoreMCP(rt.ctx); err != nil && rt.ctx.Err() == nil {
			global.Logger.Error("restore MCP configuration failed")
		}
	}()
}

// startMCPRecovery 启动 MCP 自动重连监督器：启动恢复失败或运行中断开的
// 服务会按指数退避持续重连，直到恢复、停用、删除或应用退出。
func (rt *appRuntime) startMCPRecovery() {
	rt.recoveryUp = true
	go func() {
		defer close(rt.recoveryDone)
		rt.systemSvc.RunMCPRecovery(rt.ctx, servicesystem.DefaultMCPRecoveryOptions)
	}()
}

// newEngine 装配业务 Gin 引擎并返回带准入控制的 Handler。
func (rt *appRuntime) newEngine(engine *gin.Engine) (http.Handler, error) {
	global.Metrics = pkg.NewPrometheusRegistry()
	global.Logger.Info("start init register metrics")
	if err := pkg.RegisterMetrics(engine, global.Metrics, fmt.Sprintf("%s/metrics", global.Cfg.RouterPrefix)); err != nil {
		return nil, rt.fail(fmt.Errorf("register metrics endpoint: %w", err))
	}

	global.Logger.Info("start init register gin engine")
	router.InitRouter(engine)
	return rt.admissionHandler(engine), nil
}

// admissionHandler 在停止准入后拒绝新请求，并在 Handler 返回前保持计数，
// 让关闭流程等到全部请求结束再释放数据库。
func (rt *appRuntime) admissionHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rt.gate.Enter() {
			dtocode.WriteHTTP(w, http.StatusServiceUnavailable, dtocode.ServiceStopping)
			return
		}
		defer rt.gate.Leave()
		next.ServeHTTP(w, r)
	})
}

// beginShutdown 停止接纳新任务并取消连接与后台任务；重复调用是安全的。
func (rt *appRuntime) beginShutdown() {
	rt.gate.Stop()
	serviceagent.Shutdown()
	if rt.titlePublisher != nil {
		rt.titlePublisher.Close()
	}
	rt.cancel()
}

// close 等待已取得的资源释放：先等 MCP 恢复与重连监督协程退出，再关闭 MCP 连接、
// 数据库连接与日志。重复调用只执行一次。
func (rt *appRuntime) close() {
	rt.closeOnce.Do(func() {
		rt.cancel()
		if rt.restoreUp {
			<-rt.restoreDone
		}
		if rt.recoveryUp {
			<-rt.recoveryDone
		}
		if rt.modelSyncUp {
			<-rt.modelSyncDone
		}
		// 先停止全局指令监听，避免关停期间继续发布新版本。
		if manager := rt.instructions; manager != nil {
			manager.Close()
			<-manager.Done()
		}
		// 再等待标题 worker 退出（执行中的任务会还原为 pending），然后关闭数据库与日志。
		if queue := rt.titleQueue; queue != nil {
			queue.Close()
			<-queue.Done()
		}
		// 再等待 Memory worker 退出，最后关闭数据库与日志。
		if rt.memoryUp {
			<-rt.memoryDone
		}
		agentmcp.Default.Close()
		if rt.dbReady && db.EntClient != nil {
			_ = db.EntClient.Close()
		}
		if rt.logger != nil {
			_ = rt.logger.Sync()
		}
	})
}

// fail 记录初始化错误并返回原错误；logger 尚未就绪时只返回错误。
func (rt *appRuntime) fail(err error) error {
	if rt.logger != nil {
		rt.logger.Error("application initialization failed", zap.Error(err))
	}
	return err
}
