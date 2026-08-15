package controlledaccount

import (
	"database/sql"

	adminhandler "github.com/silent-QAQ/redstoneapi/internal/handler/admin"
	"github.com/silent-QAQ/redstoneapi/internal/service"
)

// ProvideVerificationScheduler 提供验真调度器
func ProvideVerificationScheduler(db *sql.DB, legacyService *Service) *VerificationScheduler {
	_ = legacyService.cipher // unused for now
	verifier := NewAccountVerifier()
	scheduler := NewVerificationScheduler(db, verifier, legacyService)
	return scheduler
}

func ProvideHandler(
	legacyService *Service,
	db *sql.DB,
	adminService service.AdminService,
	oauthService *service.OAuthService,
	openaiOAuthService *service.OpenAIOAuthService,
	geminiOAuthService *service.GeminiOAuthService,
	antigravityOAuthService *service.AntigravityOAuthService,
	grokOAuthTokenService service.GrokOAuthTokenService,
	grokOAuthService *service.GrokOAuthService,
	rateLimitService *service.RateLimitService,
	accountUsageService *service.AccountUsageService,
	accountTestService *service.AccountTestService,
	concurrencyService *service.ConcurrencyService,
	crsSyncService *service.CRSSyncService,
	sessionLimitCache service.SessionLimitCache,
	rpmCache service.RPMCache,
	tokenCacheInvalidator service.TokenCacheInvalidator,
	grokQuotaService *service.GrokQuotaService,
	scheduledTestService *service.ScheduledTestService,
) *Handler {
	ownedAdminService := newOwnedAdminService(adminService, db)
	ownedAccountHandler := adminhandler.ProvideAccountHandler(
		ownedAdminService,
		oauthService,
		openaiOAuthService,
		geminiOAuthService,
		antigravityOAuthService,
		grokOAuthTokenService,
		rateLimitService,
		accountUsageService,
		accountTestService,
		concurrencyService,
		crsSyncService,
		sessionLimitCache,
		rpmCache,
		tokenCacheInvalidator,
		grokQuotaService,
	)
	ownedOAuthHandler := adminhandler.NewOAuthHandler(oauthService)
	ownedOpenAIHandler := adminhandler.NewOpenAIOAuthHandler(openaiOAuthService, ownedAdminService, nil, rateLimitService)
	ownedGeminiHandler := adminhandler.NewGeminiOAuthHandler(geminiOAuthService)
	ownedAntigravityHandler := adminhandler.NewAntigravityOAuthHandler(antigravityOAuthService)
	ownedGrokHandler := adminhandler.NewGrokOAuthHandler(grokOAuthService, ownedAdminService, grokQuotaService, nil)
	
	// 创建验真调度器
	scheduler := ProvideVerificationScheduler(db, legacyService)
	
	return &Handler{
		service:                 legacyService,
		scheduler:               scheduler,
		ownedAdminService:       ownedAdminService,
		ownedAccountHandler:     ownedAccountHandler,
		ownedOAuthHandler:       ownedOAuthHandler,
		ownedOpenAIHandler:      ownedOpenAIHandler,
		ownedGeminiHandler:      ownedGeminiHandler,
		ownedAntigravityHandler: ownedAntigravityHandler,
		ownedGrokHandler:        ownedGrokHandler,
		oauthService:            oauthService,
		openaiOAuthService:      openaiOAuthService,
		geminiOAuthService:      geminiOAuthService,
		antigravityOAuthService: antigravityOAuthService,
		grokOAuthService:        grokOAuthService,
		adminService:            adminService,
		oauthBindingStore:       newOAuthBindingStore(),
		scheduledTestService:    scheduledTestService,
		crsSyncService:          crsSyncService,
	}
}
