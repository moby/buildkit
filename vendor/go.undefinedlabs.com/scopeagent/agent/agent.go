package agent

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mitchellh/go-homedir"
	"github.com/opentracing/opentracing-go"

	"go.undefinedlabs.com/scopeagent/env"
	scopeError "go.undefinedlabs.com/scopeagent/errors"
	"go.undefinedlabs.com/scopeagent/instrumentation"
	scopetesting "go.undefinedlabs.com/scopeagent/instrumentation/testing"
	"go.undefinedlabs.com/scopeagent/reflection"
	"go.undefinedlabs.com/scopeagent/runner"
	"go.undefinedlabs.com/scopeagent/tags"
	"go.undefinedlabs.com/scopeagent/tracer"
)

type (
	Agent struct {
		tracer opentracing.Tracer

		apiEndpoint string
		apiKey      string

		agentId          string
		version          string
		metadata         map[string]interface{}
		debugMode        bool
		testingMode      bool
		setGlobalTracer  bool
		panicAsFail      bool
		failRetriesCount int

		recorder         *SpanRecorder
		recorderFilename string
		flushFrequency   time.Duration

		optionalRecorders []tracer.SpanRecorder

		userAgent string
		agentType string

		logger          *log.Logger
		printReportOnce sync.Once
	}

	Option func(*Agent)
)

var (
	version = "0.2.0"

	testingModeFrequency    = time.Second
	nonTestingModeFrequency = time.Minute
)

func WithApiKey(apiKey string) Option {
	return func(agent *Agent) {
		agent.apiKey = apiKey
	}
}

func WithApiEndpoint(apiEndpoint string) Option {
	return func(agent *Agent) {
		agent.apiEndpoint = apiEndpoint
	}
}

func WithServiceName(service string) Option {
	return func(agent *Agent) {
		agent.metadata[tags.Service] = service
	}
}

func WithDebugEnabled() Option {
	return func(agent *Agent) {
		agent.debugMode = true
	}
}

func WithTestingModeEnabled() Option {
	return func(agent *Agent) {
		agent.testingMode = true
	}
}

func WithSetGlobalTracer() Option {
	return func(agent *Agent) {
		agent.setGlobalTracer = true
	}
}

func WithMetadata(values map[string]interface{}) Option {
	return func(agent *Agent) {
		for k, v := range values {
			agent.metadata[k] = v
		}
	}
}

func WithGitInfo(repository string, commitSha string, sourceRoot string) Option {
	return func(agent *Agent) {
		agent.metadata[tags.Repository] = repository
		agent.metadata[tags.Commit] = commitSha
		agent.metadata[tags.SourceRoot] = sourceRoot
	}
}

func WithUserAgent(userAgent string) Option {
	return func(agent *Agent) {
		userAgent = strings.TrimSpace(userAgent)
		if userAgent != "" {
			agent.userAgent = userAgent
		}
	}
}

func WithAgentType(agentType string) Option {
	return func(agent *Agent) {
		agentType = strings.TrimSpace(agentType)
		if agentType != "" {
			agent.agentType = agentType
		}
	}
}

func WithConfigurationKeys(keys []string) Option {
	return func(agent *Agent) {
		if keys != nil && len(keys) > 0 {
			agent.metadata[tags.ConfigurationKeys] = keys
		}
	}
}

func WithConfiguration(values map[string]interface{}) Option {
	return func(agent *Agent) {
		if values == nil {
			return
		}
		var keys []string
		for k, v := range values {
			agent.metadata[k] = v
			keys = append(keys, k)
		}
		agent.metadata[tags.ConfigurationKeys] = keys
	}
}

func WithRetriesOnFail(retriesCount int) Option {
	return func(agent *Agent) {
		agent.failRetriesCount = retriesCount
	}
}

func WithHandlePanicAsFail() Option {
	return func(agent *Agent) {
		agent.panicAsFail = true
	}
}

func WithRecorders(recorders ...tracer.SpanRecorder) Option {
	return func(agent *Agent) {
		agent.optionalRecorders = recorders
	}
}

func WithGlobalPanicHandler() Option {
	return func(agent *Agent) {
		reflection.AddPanicHandler(func(e interface{}) {
			instrumentation.Logger().Printf("Panic handler triggered by: %v.\nFlushing agent, sending partial results...", scopeError.GetCurrentError(e).ErrorStack())
			agent.Flush()
		})
		reflection.AddOnPanicExitHandler(func(e interface{}) {
			instrumentation.Logger().Printf("Process is going to end by: %v,\nStopping agent...", scopeError.GetCurrentError(e).ErrorStack())
			scopetesting.PanicAllRunningTests(e, 3)
			agent.Stop()
		})
	}
}

// Creates a new Scope Agent instance
func NewAgent(options ...Option) (*Agent, error) {
	agent := new(Agent)
	agent.metadata = make(map[string]interface{})
	agent.version = version
	agent.agentId = generateAgentID()
	agent.userAgent = fmt.Sprintf("scope-agent-go/%s", agent.version)
	agent.panicAsFail = false
	agent.failRetriesCount = 0

	for _, opt := range options {
		opt(agent)
	}

	if err := agent.setupLogging(); err != nil {
		agent.logger = log.New(ioutil.Discard, "", 0)
	}

	agent.debugMode = agent.debugMode || env.ScopeDebug.Value

	configProfile := GetConfigCurrentProfile()

	if agent.apiKey == "" || agent.apiEndpoint == "" {
		if dsn, set := env.ScopeDsn.Tuple(); set && dsn != "" {
			dsnApiKey, dsnApiEndpoint, dsnErr := parseDSN(dsn)
			if dsnErr != nil {
				agent.logger.Printf("Error parsing dsn value: %v\n", dsnErr)
			} else {
				agent.apiKey = dsnApiKey
				agent.apiEndpoint = dsnApiEndpoint
			}
		} else {
			agent.logger.Println("environment variable $SCOPE_DSN not found")
		}
	}

	if agent.apiKey == "" {
		if apiKey, set := env.ScopeApiKey.Tuple(); set && apiKey != "" {
			agent.apiKey = apiKey
		} else if configProfile != nil {
			agent.logger.Println("API key found in the native app configuration")
			agent.apiKey = configProfile.ApiKey
		} else {
			agent.logger.Println("API key not found, agent can't be started")
			return nil, errors.New(fmt.Sprintf("There was a problem initializing Scope.\n"+
				"Check the agent logs at %s for more information.\n", agent.recorderFilename))
		}
	}

	if agent.apiEndpoint == "" {
		if endpoint, set := env.ScopeApiEndpoint.Tuple(); set && endpoint != "" {
			agent.apiEndpoint = endpoint
		} else if configProfile != nil {
			agent.logger.Println("API endpoint found in the native app configuration")
			agent.apiEndpoint = configProfile.ApiEndpoint
		} else {
			agent.logger.Printf("using default endpoint: %v\n", endpoint)
			agent.apiEndpoint = endpoint
		}
	}

	// Agent data
	if agent.agentType == "" {
		agent.agentType = "go"
	}
	agent.metadata[tags.AgentID] = agent.agentId
	agent.metadata[tags.AgentVersion] = version
	agent.metadata[tags.AgentType] = agent.agentType
	agent.metadata[tags.TestingMode] = agent.testingMode

	// Platform data
	agent.metadata[tags.PlatformName] = runtime.GOOS
	agent.metadata[tags.PlatformArchitecture] = runtime.GOARCH
	if runtime.GOARCH == "amd64" {
		agent.metadata[tags.ProcessArchitecture] = "X64"
	} else if runtime.GOARCH == "386" {
		agent.metadata[tags.ProcessArchitecture] = "X86"
	} else if runtime.GOARCH == "arm" {
		agent.metadata[tags.ProcessArchitecture] = "Arm"
	} else if runtime.GOARCH == "arm64" {
		agent.metadata[tags.ProcessArchitecture] = "Arm64"
	}

	// Current folder
	wd, _ := os.Getwd()
	agent.metadata[tags.CurrentFolder] = filepath.Clean(wd)

	// Hostname
	hostname, _ := os.Hostname()
	agent.metadata[tags.Hostname] = hostname

	// Go version
	agent.metadata[tags.GoVersion] = runtime.Version()

	// Service name
	addElementToMapIfEmpty(agent.metadata, tags.Service, env.ScopeService.Value)

	// Configurations
	addElementToMapIfEmpty(agent.metadata, tags.ConfigurationKeys, env.ScopeConfiguration.Value)

	// Metadata
	addToMapIfEmpty(agent.metadata, env.ScopeMetadata.Value)

	// Git data
	addToMapIfEmpty(agent.metadata, getGitInfoFromEnv())
	addToMapIfEmpty(agent.metadata, getCIMetadata())
	addToMapIfEmpty(agent.metadata, getGitInfoFromGitFolder())

	agent.metadata[tags.Diff] = getGitDiff()

	agent.metadata[tags.InContainer] = isRunningInContainer()

	// Dependencies
	agent.metadata[tags.Dependencies] = getDependencyMap()

	// Expand '~' in source root
	var sourceRoot string
	if sRoot, ok := agent.metadata[tags.SourceRoot]; ok {
		cSRoot := sRoot.(string)
		cSRoot = filepath.Clean(cSRoot)
		if sRootEx, err := homedir.Expand(cSRoot); err == nil {
			cSRoot = sRootEx
		}
		sourceRoot = cSRoot
	}
	if sourceRoot == "" {
		sourceRoot = getGoModDir()
	}
	agent.metadata[tags.SourceRoot] = sourceRoot

	if !agent.testingMode {
		if env.ScopeTestingMode.IsSet {
			agent.testingMode = env.ScopeTestingMode.Value
		} else {
			agent.testingMode = agent.metadata[tags.CI].(bool)
		}
	}

	if agent.failRetriesCount == 0 {
		agent.failRetriesCount = env.ScopeTestingFailRetries.Value
	}
	agent.panicAsFail = agent.panicAsFail || env.ScopeTestingPanicAsFail.Value

	if agent.debugMode {
		agent.logMetadata()
	}

	agent.flushFrequency = nonTestingModeFrequency
	if agent.testingMode {
		agent.flushFrequency = testingModeFrequency
	}
	agent.recorder = NewSpanRecorder(agent)
	var recorder tracer.SpanRecorder = agent.recorder
	if agent.optionalRecorders != nil {
		recorders := append(agent.optionalRecorders, agent.recorder)
		recorder = tracer.NewMultiRecorder(recorders...)
	}

	agent.tracer = tracer.NewWithOptions(tracer.Options{
		Recorder: recorder,
		ShouldSample: func(traceID uint64) bool {
			return true
		},
		MaxLogsPerSpan: 10000,
		// Log the error in the current span
		OnSpanFinishPanic: scopeError.WriteExceptionEventInRawSpan,
	})
	instrumentation.SetTracer(agent.tracer)
	instrumentation.SetLogger(agent.logger)
	instrumentation.SetSourceRoot(sourceRoot)
	if agent.setGlobalTracer || env.ScopeTracerGlobal.Value {
		opentracing.SetGlobalTracer(agent.Tracer())
	}

	return agent, nil
}

func getGoModDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return filepath.Dir("/")
	}
	for {
		rel, _ := filepath.Rel("/", dir)
		// Exit the loop once we reach the basePath.
		if rel == "." {
			return filepath.Dir("/")
		}
		modPath := fmt.Sprintf("%v/go.mod", dir)
		if _, err := os.Stat(modPath); err == nil {
			return dir
		}
		// Going up!
		dir += "/.."
	}
}

func (a *Agent) setupLogging() error {
	filename := fmt.Sprintf("scope-go-%s-%s.log", time.Now().Format("20060102150405"), a.agentId)
	dir, err := getLogPath()
	if err != nil {
		return err
	}
	a.recorderFilename = filepath.Join(dir, filename)

	file, err := os.OpenFile(a.recorderFilename, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return err
	}

	a.logger = log.New(file, "", log.LstdFlags|log.Lshortfile)
	return nil
}

func (a *Agent) Tracer() opentracing.Tracer {
	return a.tracer
}

func (a *Agent) Logger() *log.Logger {
	return a.logger
}

// Runs the test suite
func (a *Agent) Run(m *testing.M) int {
	defer a.Stop()
	return runner.Run(m, runner.Options{
		FailRetries: a.failRetriesCount,
		PanicAsFail: a.panicAsFail,
		Logger:      a.logger,
		OnPanic: func(t *testing.T, err interface{}) {
			if t != nil {
				a.logger.Printf("test '%s' has panicked (%v), stopping agent", t.Name(), err)
			} else {
				a.logger.Printf("panic: %v", err)
			}
			a.Stop()
		},
	})
}

// Stops the agent
func (a *Agent) Stop() {
	a.logger.Println("Scope agent is stopping gracefully...")
	if a.recorder != nil {
		a.recorder.Stop()
	}
	a.PrintReport()
}

// Flush agent buffer
func (a *Agent) Flush() {
	a.logger.Println("Flushing agent buffer...")
	if a.recorder != nil {
		if err := a.recorder.Flush(); err != nil {
			a.logger.Println(err)
		}
	}
}

func generateAgentID() string {
	agentId, err := uuid.NewRandom()
	if err != nil {
		panic(err)
	}
	return agentId.String()
}

func getLogPath() (string, error) {
	if env.ScopeLoggerRoot.IsSet {
		return env.ScopeLoggerRoot.Value, nil
	}

	logFolder := ""
	if runtime.GOOS == "linux" {
		logFolder = "/var/log/scope"
	} else {
		homeDir, err := homedir.Dir()
		if err != nil {
			return "", err
		}
		if runtime.GOOS == "windows" {
			logFolder = fmt.Sprintf("%s/AppData/Roaming/scope/logs", homeDir)
		} else if runtime.GOOS == "darwin" {
			logFolder = fmt.Sprintf("%s/Library/Logs/Scope", homeDir)
		}
	}

	if logFolder != "" {
		if _, err := os.Stat(logFolder); err == nil {
			return logFolder, nil
		} else if os.IsNotExist(err) && os.Mkdir(logFolder, 0755) == nil {
			return logFolder, nil
		}
	}

	// If the log folder can't be used we return a temporal path, so we don't miss the agent logs
	logFolder = filepath.Join(os.TempDir(), "scope")
	if _, err := os.Stat(logFolder); err == nil {
		return logFolder, nil
	} else if os.IsNotExist(err) && os.Mkdir(logFolder, 0755) == nil {
		return logFolder, nil
	} else {
		return "", err
	}
}

func parseDSN(dsnString string) (apiKey string, apiEndpoint string, err error) {
	uri, err := url.Parse(dsnString)
	if err != nil {
		return "", "", err
	}
	if uri.User != nil {
		apiKey = uri.User.Username()
	}
	uri.User = nil
	apiEndpoint = uri.String()
	return
}

func (a *Agent) getUrl(pathValue string) string {
	uri, err := url.Parse(a.apiEndpoint)
	if err != nil {
		a.logger.Fatal(err)
	}
	uri.Path = path.Join(uri.Path, pathValue)
	return uri.String()
}
