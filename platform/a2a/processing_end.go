package a2a

import (
	"context"
	"fmt"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/chenhg5/cc-connect/core"
)

func (p *Platform) OnProcessingEnd(_ context.Context, replyCtx any, _ core.ProcessingEndEvent) error {
	taskID := taskIDFromReplyCtx(replyCtx)
	if taskID == "" {
		return fmt.Errorf("a2a: unsupported processing-end context %T", replyCtx)
	}
	if !p.finishTask(taskID, pendingResult{state: sdka2a.TaskStateCompleted}) {
		return nil
	}
	return nil
}
