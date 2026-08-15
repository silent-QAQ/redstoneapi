package service

import (
	"github.com/silent-QAQ/redstoneapi/internal/config"
	"github.com/silent-QAQ/redstoneapi/internal/util/responseheaders"
)

func compileResponseHeaderFilter(cfg *config.Config) *responseheaders.CompiledHeaderFilter {
	if cfg == nil {
		return nil
	}
	return responseheaders.CompileHeaderFilter(cfg.Security.ResponseHeaders)
}
