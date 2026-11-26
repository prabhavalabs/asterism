package api

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/gen/asterism/v1/asterismv1connect"
	"github.com/prabhavalabs/asterism/internal/service"
)

// Handler provides thin ConnectRPC adapters delegating all domain logic to internal/service.
type Handler struct {
	svc *service.Observatory
}

func New(svc *service.Observatory) *Handler {
	return &Handler{svc: svc}
}

// Register mounts all ConnectRPC service handlers on the provided ServeMux,
// wrapped with the given auth middleware.
func (h *Handler) Register(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mount := func(path string, handler http.Handler) {
		if wrap != nil {
			handler = wrap(handler)
		}
		mux.Handle(path, handler)
	}

	mount(asterismv1connect.NewInventoryServiceHandler(h))
	mount(asterismv1connect.NewMetricsServiceHandler(h))
	mount(asterismv1connect.NewTopologyServiceHandler(h))
	mount(asterismv1connect.NewEventsServiceHandler(h))
	mount(asterismv1connect.NewLogsServiceHandler(h))
	mount(asterismv1connect.NewLiveServiceHandler(h))
	mount(asterismv1connect.NewDockerServiceHandler(h))
	mount(asterismv1connect.NewAgentAdminServiceHandler(h))
	// State-changing RPCs require the Connect-Protocol-Version header as a CSRF guard
	// (a cross-site form or simple request cannot set it); connect-web sends it.
	mount(asterismv1connect.NewActionsServiceHandler(h, connect.WithRequireConnectProtocolHeader()))
}

func (h *Handler) GetOverview(ctx context.Context, _ *connect.Request[asterismv1.GetOverviewRequest]) (*connect.Response[asterismv1.GetOverviewResponse], error) {
	return connect.NewResponse(h.svc.Overview(principal(ctx))), nil
}

func (h *Handler) ListProjects(_ context.Context, req *connect.Request[asterismv1.ListProjectsRequest]) (*connect.Response[asterismv1.ListProjectsResponse], error) {
	return respond(h.svc.ListProjectsPage(req.Msg))
}

func (h *Handler) GetProject(_ context.Context, req *connect.Request[asterismv1.GetProjectRequest]) (*connect.Response[asterismv1.GetProjectResponse], error) {
	proj, err := h.svc.GetProject(req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&asterismv1.GetProjectResponse{Project: proj}), nil
}

func (h *Handler) ListWorkloads(_ context.Context, req *connect.Request[asterismv1.ListWorkloadsRequest]) (*connect.Response[asterismv1.ListWorkloadsResponse], error) {
	return respond(h.svc.ListWorkloadsPage(req.Msg))
}

func (h *Handler) GetWorkload(_ context.Context, req *connect.Request[asterismv1.GetWorkloadRequest]) (*connect.Response[asterismv1.GetWorkloadResponse], error) {
	resp, err := h.svc.GetWorkload(req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(resp), nil
}

func (h *Handler) ListPorts(_ context.Context, req *connect.Request[asterismv1.ListPortsRequest]) (*connect.Response[asterismv1.ListPortsResponse], error) {
	return respond(h.svc.ListPortsPage(req.Msg))
}

func (h *Handler) ListIngress(_ context.Context, req *connect.Request[asterismv1.ListIngressRequest]) (*connect.Response[asterismv1.ListIngressResponse], error) {
	return respond(h.svc.ListIngressPage(req.Msg))
}

func (h *Handler) QueryRange(_ context.Context, req *connect.Request[asterismv1.QueryRangeRequest]) (*connect.Response[asterismv1.QueryRangeResponse], error) {
	return connect.NewResponse(&asterismv1.QueryRangeResponse{
		Series: h.svc.QueryRange(req.Msg.EntityId, req.Msg.Metrics, req.Msg.FromMs, req.Msg.ToMs),
	}), nil
}

func (h *Handler) TopN(_ context.Context, req *connect.Request[asterismv1.TopNRequest]) (*connect.Response[asterismv1.TopNResponse], error) {
	return connect.NewResponse(&asterismv1.TopNResponse{
		Items: h.svc.TopN(req.Msg.Metric, req.Msg.Limit),
	}), nil
}

func (h *Handler) GetGraph(_ context.Context, req *connect.Request[asterismv1.GetGraphRequest]) (*connect.Response[asterismv1.GetGraphResponse], error) {
	return connect.NewResponse(&asterismv1.GetGraphResponse{
		Graph: h.svc.Graph(req.Msg.Category),
	}), nil
}

func (h *Handler) ListEvents(_ context.Context, req *connect.Request[asterismv1.ListEventsRequest]) (*connect.Response[asterismv1.ListEventsResponse], error) {
	return respond(h.svc.ListEventsPage(req.Msg))
}

func (h *Handler) TailLogs(_ context.Context, req *connect.Request[asterismv1.TailLogsRequest]) (*connect.Response[asterismv1.TailLogsResponse], error) {
	return connect.NewResponse(h.svc.TailLogs(req.Msg)), nil
}

type seriesKey struct {
	entityID string
	metric   string
}

func (h *Handler) Subscribe(
	ctx context.Context,
	req *connect.Request[asterismv1.SubscribeRequest],
	stream *connect.ServerStream[asterismv1.SubscribeResponse],
) error {
	interval := time.Duration(req.Msg.IntervalMs) * time.Millisecond
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 10*time.Second {
		interval = 10 * time.Second
	}

	ch, unsub := h.svc.SubscribeBus()
	defer unsub()

	intern := make(map[seriesKey]uint32)
	var nextID uint32 = 1

	pending := make(map[seriesKey]float32)
	var pendingTS int64

	flushTicker := time.NewTicker(interval)
	defer flushTicker.Stop()

	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-ch:
			for _, ev := range msg.Events {
				if err := stream.Send(&asterismv1.SubscribeResponse{
					Payload: &asterismv1.SubscribeResponse_Event{Event: ev},
				}); err != nil {
					return err
				}
			}
			for _, s := range msg.Samples {
				pending[seriesKey{entityID: s.EntityID, metric: s.Metric}] = float32(s.Value)
				if s.TSMs > pendingTS {
					pendingTS = s.TSMs
				}
			}
		case <-flushTicker.C:
			if len(pending) == 0 {
				continue
			}

			var newEntries []*asterismv1.SeriesEntry
			seriesIDs := make([]uint32, 0, len(pending))
			values := make([]float32, 0, len(pending))

			for k, val := range pending {
				id, ok := intern[k]
				if !ok {
					id = nextID
					nextID++
					intern[k] = id
					newEntries = append(newEntries, &asterismv1.SeriesEntry{
						SeriesId: id,
						EntityId: k.entityID,
						Metric:   k.metric,
					})
				}
				seriesIDs = append(seriesIDs, id)
				values = append(values, val)
			}

			if len(newEntries) > 0 {
				if err := stream.Send(&asterismv1.SubscribeResponse{
					Payload: &asterismv1.SubscribeResponse_SeriesDict{
						SeriesDict: &asterismv1.SeriesDict{Entries: newEntries},
					},
				}); err != nil {
					return err
				}
			}

			if err := stream.Send(&asterismv1.SubscribeResponse{
				Payload: &asterismv1.SubscribeResponse_MetricBatch{
					MetricBatch: &asterismv1.MetricBatch{
						TsMs:      pendingTS,
						SeriesIds: seriesIDs,
						Values:    values,
					},
				},
			}); err != nil {
				return err
			}

			clear(pending)
		case now := <-heartbeatTicker.C:
			if err := stream.Send(&asterismv1.SubscribeResponse{
				Payload: &asterismv1.SubscribeResponse_Heartbeat{
					Heartbeat: &asterismv1.Heartbeat{TsMs: now.UnixMilli()},
				},
			}); err != nil {
				return err
			}
		}
	}
}
