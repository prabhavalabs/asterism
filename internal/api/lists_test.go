package api

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestListRPCsPageOverTheWire(t *testing.T) {
	srv, toks := newTestServer(t, false)
	docker, _, inv := client(srv, toks["reader"], true)
	ctx := context.Background()

	first, err := docker.ListContainers(ctx, connect.NewRequest(&asterismv1.ListContainersRequest{
		Page: &asterismv1.PageRequest{PageSize: 10, Page: 1},
	}))
	if err != nil {
		t.Fatal(err)
	}
	info := first.Msg.PageInfo
	if len(first.Msg.Containers) != 10 || info.TotalSize < 30 || info.TotalPages < 3 || first.Msg.Counts.Total != uint32(info.TotalSize) {
		t.Fatalf("page 1: n=%d info=%+v counts=%+v", len(first.Msg.Containers), info, first.Msg.Counts)
	}
	if first.Msg.Capabilities == nil {
		t.Fatal("capabilities missing")
	}
	second, err := docker.ListContainers(ctx, connect.NewRequest(&asterismv1.ListContainersRequest{
		Page: &asterismv1.PageRequest{PageSize: 10, Page: 2},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if second.Msg.Containers[0].Id == first.Msg.Containers[0].Id {
		t.Fatal("pages overlap")
	}

	// Omitting page keeps the legacy "whole (bounded) list" behaviour.
	all, err := docker.ListContainers(ctx, connect.NewRequest(&asterismv1.ListContainersRequest{}))
	if err != nil || int32(len(all.Msg.Containers)) != info.TotalSize || all.Msg.PageInfo.PageSize != 200 {
		t.Fatalf("unpaged: n=%d info=%+v err=%v", len(all.Msg.Containers), all.Msg.PageInfo, err)
	}

	_, err = docker.ListImages(ctx, connect.NewRequest(&asterismv1.ListImagesRequest{OrderBy: "weight desc"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "allowed: size, created, repository, containers") {
		t.Fatalf("bad order_by: %v", err)
	}

	ports, err := inv.ListPorts(ctx, connect.NewRequest(&asterismv1.ListPortsRequest{
		Exposure: asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, Page: &asterismv1.PageRequest{},
	}))
	if err != nil {
		t.Fatal(err)
	}
	c := ports.Msg.ExposureCounts
	if c.Total != c.Public+c.Loopback+c.Internal || ports.Msg.PageInfo.TotalSize != int32(c.Public) || ports.Msg.PageInfo.PageSize != 25 {
		t.Fatalf("ports facets: %+v info=%+v", c, ports.Msg.PageInfo)
	}
}
