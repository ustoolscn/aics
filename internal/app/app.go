package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aics/internal/config"
	"aics/internal/embedding"
	"aics/internal/feishu"
	"aics/internal/feishuhistory"
	"aics/internal/imagehost"
	"aics/internal/knowledge"
	"aics/internal/llm"
	"aics/internal/orchestrator"
	"aics/internal/prompt"
	"aics/internal/session"
	"aics/internal/skill"
	"aics/internal/tool"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

type App struct {
	cfg    config.Config
	logger *slog.Logger
	bot    *feishu.Bot
	server *http.Server
	db     *sql.DB
	redis  *redis.Client
}

func New(cfg config.Config) *App {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	store := setupSessionStore(cfg, logger)
	promptLoader := prompt.NewLoader(cfg.PromptFile)
	llmClient := llm.NewOpenAIClient(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMTimeout, cfg.LogLLMRequest)
	imageUploader := imagehost.NewClient(cfg.ImageHost)
	registeredTools := []tool.Tool{
		tool.NewKnowledgeSearch(cfg.KnowledgeDir),
	}
	db := setupKnowledgeTool(cfg, logger, &registeredTools)
	var skillIndexPrompt string
	if cfg.SkillsEnabled {
		skills, err := skill.NewLoader(cfg.SkillsDir).LoadAll()
		if err != nil {
			logger.Error("load skills failed", "dir", cfg.SkillsDir, "err", err)
		} else if len(skills) > 0 {
			runner := skill.NewRunner(skills, llmClient, cfg.SkillMaxSteps, cfg.SkillScriptTimeout)
			registeredTools = append(registeredTools, skill.NewTool(runner, skills))
			skillIndexPrompt = skill.BuildIndexPrompt(skills)
			logger.Info("skills loaded", "dir", cfg.SkillsDir, "count", len(skills))
		} else {
			logger.Info("no skills loaded", "dir", cfg.SkillsDir)
		}
	}
	tools := tool.NewRegistry(registeredTools...)
	orch := orchestrator.New(store, promptLoader, llmClient, tools, cfg.MaxHistoryMessages)
	orch.SetSkillIndexPrompt(skillIndexPrompt)
	deduper, redisClient := setupDeduper(cfg, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	bot := feishu.NewBot(
		cfg.FeishuAppID,
		cfg.FeishuAppSecret,
		cfg.FeishuVerifyToken,
		cfg.FeishuEncryptKey,
		cfg.ReactionReceived,
		cfg.ReactionProcessing,
		cfg.ReactionDone,
		cfg.LogRawMessage,
		cfg.ReplyFormat,
		cfg.ReplyMarkdownMargin,
		cfg.LLMStream,
		cfg.StreamPlaceholder,
		cfg.StreamUpdateEvery,
		deduper,
		cfg.ImageInputMode,
		imageUploader,
		orch,
		logger,
	)
	mux.HandleFunc(cfg.WebhookPath, bot.WebhookHandler())

	return &App{
		cfg:    cfg,
		logger: logger,
		bot:    bot,
		server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		db:    db,
		redis: redisClient,
	}
}

func setupSessionStore(cfg config.Config, logger *slog.Logger) session.Store {
	if cfg.HistorySource == "feishu" {
		logger.Info("using feishu topic history", "lookback", cfg.HistoryLookback, "max_history", cfg.MaxHistoryMessages)
		return feishuhistory.NewStore(cfg.FeishuAppID, cfg.FeishuAppSecret, cfg.HistoryLookback, logger)
	}
	logger.Info("using memory session history", "max_history", cfg.MaxHistoryMessages)
	return session.NewMemoryStore()
}

func setupDeduper(cfg config.Config, logger *slog.Logger) (feishu.Deduper, *redis.Client) {
	if cfg.DedupeStore != "redis" {
		logger.Info("using memory message dedupe", "ttl", cfg.DedupeTTL)
		return feishu.NewMemoryDeduper(cfg.DedupeTTL), nil
	}
	if cfg.RedisConnString == "" {
		logger.Warn("DEDUPE_STORE=redis but REDIS_CONN_STRING is empty; using memory message dedupe")
		return feishu.NewMemoryDeduper(cfg.DedupeTTL), nil
	}
	opts, err := redis.ParseURL(cfg.RedisConnString)
	if err != nil {
		logger.Error("parse REDIS_CONN_STRING failed; using memory message dedupe", "err", err)
		return feishu.NewMemoryDeduper(cfg.DedupeTTL), nil
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		logger.Error("ping redis failed; using memory message dedupe", "err", err)
		_ = client.Close()
		return feishu.NewMemoryDeduper(cfg.DedupeTTL), nil
	}
	logger.Info("using redis message dedupe", "addr", opts.Addr, "db", opts.DB, "ttl", cfg.DedupeTTL)
	return feishu.NewRedisDeduper(client, cfg.DedupeTTL, "aics:dedupe:feishu:"), client
}

func setupKnowledgeTool(cfg config.Config, logger *slog.Logger, registeredTools *[]tool.Tool) *sql.DB {
	if cfg.KnowledgeMode == "local" {
		logger.Info("using local markdown knowledge search", "dir", cfg.KnowledgeDir)
		return nil
	}
	if cfg.DatabaseURL == "" || cfg.EmbeddingAPIKey == "" {
		if cfg.KnowledgeMode == "rag" {
			logger.Warn("rag knowledge search requested but DATABASE_URL or EMBEDDING_API_KEY is empty; falling back to local markdown search")
		} else {
			logger.Info("rag knowledge search not configured; using local markdown search")
		}
		return nil
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Error("open database failed; falling back to local markdown search", "err", err)
		return nil
	}
	if err := db.Ping(); err != nil {
		logger.Error("ping database failed; falling back to local markdown search", "err", err)
		_ = db.Close()
		return nil
	}

	store := knowledge.NewStore(db)
	if err := store.EnsureSchema(context.Background(), cfg.EmbeddingDimensions); err != nil {
		logger.Error("ensure knowledge schema failed; falling back to local markdown search", "err", err)
		_ = db.Close()
		return nil
	}

	embedder := embedding.NewGeminiClient(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDimensions, cfg.LLMTimeout)
	(*registeredTools)[0] = tool.NewRAGKnowledgeSearch(store, embedder, cfg.KnowledgeTopK)
	logger.Info("using rag knowledge search", "base_url", cfg.EmbeddingBaseURL, "model", cfg.EmbeddingModel, "dimensions", cfg.EmbeddingDimensions, "top_k", cfg.KnowledgeTopK)
	return db
}

func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 2)
	go func() {
		a.logger.Info("starting http server", "addr", a.cfg.HTTPAddr)
		err := a.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	if a.cfg.EventMode == "websocket" {
		go func() {
			errs <- a.bot.Start(ctx)
		}()
	} else {
		a.logger.Info("feishu webhook mode enabled", "path", a.cfg.WebhookPath)
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.server.Shutdown(shutdownCtx)
		if a.db != nil {
			_ = a.db.Close()
		}
		if a.redis != nil {
			_ = a.redis.Close()
		}
		return nil
	case err := <-errs:
		if a.db != nil {
			_ = a.db.Close()
		}
		if a.redis != nil {
			_ = a.redis.Close()
		}
		if err != nil {
			return err
		}
		return nil
	}
}
