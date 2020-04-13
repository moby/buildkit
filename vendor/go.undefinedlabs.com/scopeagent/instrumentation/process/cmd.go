package process

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opentracing/opentracing-go"

	"go.undefinedlabs.com/scopeagent/instrumentation"
)

// Injects the span context to the command environment variables
func InjectToCmd(ctx context.Context, command *exec.Cmd) *exec.Cmd {
	if command.Env == nil {
		command.Env = []string{}
	}
	err := InjectFromContext(ctx, &command.Env)
	if err != nil {
		instrumentation.Logger().Println(err)
	}
	return command
}

// Injects a new span context to the command environment variables
func InjectToCmdWithSpan(ctx context.Context, command *exec.Cmd) (opentracing.Span, context.Context) {
	innerSpan, innerCtx := opentracing.StartSpanFromContextWithTracer(ctx, instrumentation.Tracer(),
		"Exec: "+getOperationNameFromArgs(command.Args))
	innerSpan.SetTag("Args", command.Args)
	innerSpan.SetTag("Path", command.Path)
	innerSpan.SetTag("Dir", command.Dir)
	InjectToCmd(innerCtx, command)
	return innerSpan, innerCtx
}

func getOperationNameFromArgs(args []string) string {
	if args == nil || len(args) == 0 {
		return ""
	}
	var operationNameBuilder = new(strings.Builder)
	operationNameBuilder.WriteString(filepath.Base(args[0]))
	operationNameBuilder.WriteRune(' ')
	for _, item := range args[1:] {
		if strings.ContainsRune(item, ' ') {
			operationNameBuilder.WriteRune('"')
			operationNameBuilder.WriteString(item)
			operationNameBuilder.WriteRune('"')
		} else {
			operationNameBuilder.WriteString(item)
		}
		operationNameBuilder.WriteRune(' ')
	}
	return operationNameBuilder.String()
}
