package llm

import (
	"context"
	"encoding/json"
)

// Mock has no I/O, tools, database or execution capability. Fixtures are copied.
type Mock struct {
	Fixture                   json.RawMessage
	InputTokens, OutputTokens int64
	Err                       error
}

func (m Mock) Complete(ctx context.Context, r Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	return Response{JSON: append(json.RawMessage(nil), m.Fixture...), InputTokens: m.InputTokens, OutputTokens: m.OutputTokens}, m.Err
}
