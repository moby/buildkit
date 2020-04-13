package env

import "go.undefinedlabs.com/scopeagent/tags"

var (
	ScopeDsn                              = newStringEnvVar("", "SCOPE_DSN")
	ScopeApiKey                           = newStringEnvVar("", "SCOPE_APIKEY")
	ScopeApiEndpoint                      = newStringEnvVar("https://app.scope.dev", "SCOPE_API_ENDPOINT")
	ScopeService                          = newStringEnvVar("default", "SCOPE_SERVICE")
	ScopeRepository                       = newStringEnvVar("", "SCOPE_REPOSITORY")
	ScopeCommitSha                        = newStringEnvVar("", "SCOPE_COMMIT_SHA")
	ScopeBranch                           = newStringEnvVar("", "SCOPE_BRANCH")
	ScopeSourceRoot                       = newStringEnvVar("", "SCOPE_SOURCE_ROOT")
	ScopeLoggerRoot                       = newStringEnvVar("", "SCOPE_LOGGER_ROOT", "SCOPE_LOG_ROOT_PATH")
	ScopeDebug                            = newBooleanEnvVar(false, "SCOPE_DEBUG")
	ScopeTracerGlobal                     = newBooleanEnvVar(false, "SCOPE_TRACER_GLOBAL", "SCOPE_SET_GLOBAL_TRACER")
	ScopeTestingMode                      = newBooleanEnvVar(false, "SCOPE_TESTING_MODE")
	ScopeTestingFailRetries               = newIntEnvVar(0, "SCOPE_TESTING_FAIL_RETRIES")
	ScopeTestingPanicAsFail               = newBooleanEnvVar(false, "SCOPE_TESTING_PANIC_AS_FAIL")
	ScopeConfiguration                    = newSliceEnvVar([]string{tags.PlatformName, tags.PlatformArchitecture, tags.GoVersion}, "SCOPE_CONFIGURATION")
	ScopeMetadata                         = newMapEnvVar(nil, "SCOPE_METADATA")
	ScopeInstrumentationHttpPayloads      = newBooleanEnvVar(false, "SCOPE_INSTRUMENTATION_HTTP_PAYLOADS")
	ScopeInstrumentationHttpStacktrace    = newBooleanEnvVar(false, "SCOPE_INSTRUMENTATION_HTTP_STACKTRACE")
	ScopeInstrumentationDbStatementValues = newBooleanEnvVar(false, "SCOPE_INSTRUMENTATION_DB_STATEMENT_VALUES")
	ScopeInstrumentationDbStacktrace      = newBooleanEnvVar(false, "SCOPE_INSTRUMENTATION_DB_STACKTRACE")
)
