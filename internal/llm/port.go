package llm

import "context"

type Client interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
}
