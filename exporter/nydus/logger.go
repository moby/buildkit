package nydus

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dragonflyoss/image-service/contrib/nydusify/pkg/converter/provider"

	"github.com/moby/buildkit/util/progress"
	"github.com/sirupsen/logrus"
)

type progressLogger struct{}

// Log outputs Nydus image exporting progress log
func (logger *progressLogger) Log(ctx context.Context, msg string, fields provider.LoggerFields) func(err error) error {
	if fields == nil {
		fields = make(provider.LoggerFields)
	}
	logrus.WithFields(fields).Info(msg)
	if len(fields) != 0 {
		var infos []string
		for key, value := range fields {
			line := fmt.Sprintf("%s=%s", key, value)
			infos = append(infos, line)
		}
		sort.Strings(infos)
		msg = msg + " [" + strings.Join(infos, " ") + "]"
	}
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(msg, st)
	return func(err error) error {
		now := time.Now()
		st.Completed = &now
		pw.Write(msg, st)
		pw.Close()
		return err
	}
}
