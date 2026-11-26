package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/prabhavalabs/asterism/internal/apiclient"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// view bundles the flags every view command shares.
type view struct {
	conn
	output
}

func (v *view) bind(cmd *cobra.Command) {
	v.conn.bind(cmd)
	v.output.bind(cmd)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
}

// run resolves the client, installs Ctrl-C handling and maps API errors to
// actionable messages.
func (v *view) run(cmd *cobra.Command, fn func(ctx context.Context, c *apiclient.Client) error) error {
	v.setup(cmd)
	c, err := v.client()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = fn(ctx, c)
	if ctx.Err() != nil && cmd.Context().Err() == nil {
		return nil // interrupted by the user: a clean exit
	}
	return friendlyError(err, c.BaseURL)
}

// call invokes a unary RPC and returns the response message.
func call[Req, Res any](ctx context.Context, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), req *Req) (*Res, error) {
	resp, err := fn(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// maxListItems caps how many items a list command collects across pages.
const maxListItems = 5000

// collect pages through a list RPC until every item (up to maxListItems) is
// fetched. fetch returns the items of one page and the page info.
func collect[T any](ctx context.Context, fetch func(ctx context.Context, page *asterismv1.PageRequest) ([]T, *asterismv1.PageInfo, error)) ([]T, *asterismv1.PageInfo, error) {
	var all []T
	var first *asterismv1.PageInfo
	for page := int32(1); ; page++ {
		items, info, err := fetch(ctx, &asterismv1.PageRequest{Page: page, PageSize: 200})
		if err != nil {
			return nil, nil, err
		}
		if first == nil {
			first = info
		}
		all = append(all, items...)
		if len(items) == 0 || page >= info.GetTotalPages() || len(all) >= maxListItems {
			break
		}
	}
	if first == nil {
		first = &asterismv1.PageInfo{}
	}
	return all, &asterismv1.PageInfo{Page: 1, PageSize: int32(len(all)), TotalSize: first.GetTotalSize(), TotalPages: 1}, nil
}
