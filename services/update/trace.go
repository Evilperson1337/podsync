package update

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

type executionIDKey struct{}

var executionCounter uint64

func withExecutionID(ctx context.Context, feedID string) (context.Context, string) {
	id := fmt.Sprintf("%s-%d-%d", feedID, time.Now().UTC().UnixNano(), atomic.AddUint64(&executionCounter, 1))
	return context.WithValue(ctx, executionIDKey{}, id), id
}

func executionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(executionIDKey{}).(string); ok {
		return value
	}
	return ""
}

func loggerWithExecution(ctx context.Context, fields log.Fields) *log.Entry {
	if fields == nil {
		fields = log.Fields{}
	}
	if executionID := executionIDFromContext(ctx); executionID != "" {
		fields["execution_id"] = executionID
	}
	return log.WithFields(fields)
}
