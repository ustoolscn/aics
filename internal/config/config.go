package config

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv              string
	HTTPAddr            string
	EventMode           string
	WebhookPath         string
	FeishuAppID         string
	FeishuAppSecret     string
	FeishuVerifyToken   string
	FeishuEncryptKey    string
	ReactionReceived    string
	ReactionProcessing  string
	ReactionDone        string
	LogRawMessage       bool
	ReplyFormat         string
	PromptFile          string
	LLMBaseURL          string
	LLMAPIKey           string
	LLMModel            string
	LLMTimeout          time.Duration
	LogLLMRequest       bool
	LLMStream           bool
	StreamPlaceholder   string
	StreamUpdateEvery   time.Duration
	MaxHistoryMessages  int
	HistorySource       string
	HistoryLookback     time.Duration
	DedupeTTL           time.Duration
	DedupeStore         string
	RedisConnString     string
	KnowledgeDir        string
	KnowledgeMode       string
	KnowledgeTopK       int
	KnowledgeChunkSize  int
	KnowledgeOverlap    int
	DatabaseURL         string
	EmbeddingBaseURL    string
	EmbeddingAPIKey     string
	EmbeddingModel      string
	EmbeddingDimensions int
	SkillsEnabled       bool
	SkillsDir           string
	SkillMaxSteps       int
	SkillScriptTimeout  time.Duration
	ImageHost           ImageHostConfig
	ImageInputMode      string
}

type ImageHostConfig struct {
	UploadURL       string
	AuthHeader      string
	AuthValue       string
	FieldName       string
	ResponseURLPath string
}

func Load() (Config, error) {
	_ = LoadDotEnv(".env")

	cfg := Config{
		AppEnv:              getenv("APP_ENV", "dev"),
		HTTPAddr:            getenv("HTTP_ADDR", ":8080"),
		EventMode:           strings.ToLower(getenv("EVENT_MODE", "webhook")),
		WebhookPath:         getenv("WEBHOOK_PATH", "/feishu/events"),
		FeishuAppID:         os.Getenv("FEISHU_APP_ID"),
		FeishuAppSecret:     os.Getenv("FEISHU_APP_SECRET"),
		FeishuVerifyToken:   os.Getenv("FEISHU_VERIFICATION_TOKEN"),
		FeishuEncryptKey:    os.Getenv("FEISHU_ENCRYPT_KEY"),
		ReactionReceived:    getenv("REACTION_RECEIVED_EMOJI", getenv("ACK_REACTION_EMOJI", "OK")),
		ReactionProcessing:  getenv("REACTION_PROCESSING_EMOJI", "Thinking"),
		ReactionDone:        getenv("REACTION_DONE_EMOJI", getenv("DONE_REACTION_EMOJI", "DONE")),
		LogRawMessage:       getenvBool("LOG_RAW_MESSAGE", true),
		ReplyFormat:         strings.ToLower(getenv("REPLY_FORMAT", "markdown_card")),
		PromptFile:          getenv("PROMPT_FILE", "configs/customer_service.md"),
		LLMBaseURL:          strings.TrimRight(getenv("LLM_BASE_URL", "https://api.openai.com/v1"), "/"),
		LLMAPIKey:           os.Getenv("LLM_API_KEY"),
		LLMModel:            getenv("LLM_MODEL", "gpt-4.1-mini"),
		LLMTimeout:          time.Duration(getenvInt("LLM_TIMEOUT_SECONDS", 60)) * time.Second,
		LogLLMRequest:       getenvBool("LOG_LLM_REQUEST", true),
		LLMStream:           getenvBool("LLM_STREAM", true),
		StreamPlaceholder:   getenv("STREAM_PLACEHOLDER", "正在处理..."),
		StreamUpdateEvery:   time.Duration(getenvInt("STREAM_UPDATE_INTERVAL_MS", 800)) * time.Millisecond,
		MaxHistoryMessages:  getenvInt("MAX_HISTORY_MESSAGES", 20),
		HistorySource:       strings.ToLower(getenv("HISTORY_SOURCE", "memory")),
		HistoryLookback:     time.Duration(getenvInt("FEISHU_HISTORY_LOOKBACK_HOURS", 168)) * time.Hour,
		DedupeTTL:           time.Duration(getenvInt("MESSAGE_DEDUPE_TTL_SECONDS", 3600)) * time.Second,
		DedupeStore:         strings.ToLower(getenv("DEDUPE_STORE", "memory")),
		RedisConnString:     os.Getenv("REDIS_CONN_STRING"),
		KnowledgeDir:        getenv("KNOWLEDGE_DIR", "knowledge"),
		KnowledgeMode:       strings.ToLower(getenv("KNOWLEDGE_MODE", "auto")),
		KnowledgeTopK:       getenvInt("KNOWLEDGE_TOP_K", 5),
		KnowledgeChunkSize:  getenvInt("KNOWLEDGE_CHUNK_SIZE", 900),
		KnowledgeOverlap:    getenvInt("KNOWLEDGE_CHUNK_OVERLAP", 120),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		EmbeddingBaseURL:    strings.TrimRight(os.Getenv("EMBEDDING_BASE_URL"), "/"),
		EmbeddingAPIKey:     getenv("EMBEDDING_API_KEY", os.Getenv("GEMINI_API_KEY")),
		EmbeddingModel:      getenv("EMBEDDING_MODEL", "gemini-embedding-2"),
		EmbeddingDimensions: getenvInt("EMBEDDING_DIMENSIONS", 768),
		SkillsEnabled:       getenvBool("SKILLS_ENABLED", true),
		SkillsDir:           getenv("SKILLS_DIR", "skills"),
		SkillMaxSteps:       getenvInt("SKILL_MAX_STEPS", 8),
		SkillScriptTimeout:  time.Duration(getenvInt("SKILL_SCRIPT_TIMEOUT_SECONDS", 30)) * time.Second,
		ImageHost: ImageHostConfig{
			UploadURL:       getenv("IMAGE_HOST_UPLOAD_URL", ""),
			AuthHeader:      getenv("IMAGE_HOST_AUTH_HEADER", ""),
			AuthValue:       getenv("IMAGE_HOST_AUTH_VALUE", ""),
			FieldName:       getenv("IMAGE_HOST_FIELD_NAME", "file"),
			ResponseURLPath: getenv("IMAGE_HOST_RESPONSE_URL_PATH", "url"),
		},
		ImageInputMode: getenv("IMAGE_INPUT_MODE", "base64"),
	}

	var missing []string
	if cfg.FeishuAppID == "" {
		missing = append(missing, "FEISHU_APP_ID")
	}
	if cfg.FeishuAppSecret == "" {
		missing = append(missing, "FEISHU_APP_SECRET")
	}
	if cfg.LLMAPIKey == "" {
		missing = append(missing, "LLM_API_KEY")
	}
	if len(missing) > 0 {
		return cfg, errors.New("missing required config: " + strings.Join(missing, ", "))
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getenvBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
	return scanner.Err()
}
