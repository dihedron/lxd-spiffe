package lxdinstance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dihedron/lxd-spiffe/internal/claim"
	"github.com/dihedron/lxd-spiffe/internal/devlxd"
	"github.com/dihedron/lxd-spiffe/internal/lxd"
	lxdinstancev1 "github.com/dihedron/lxd-spiffe/internal/proto"
)

// guest is devLXD as the agent reads it (FR-A3).
type guest struct {
	c *devlxd.Client
}

// connectDevLXD returns nil when use_devlxd is off, or auto and the socket
// is not usable.
func (s *settings) connectDevLXD(ctx context.Context) (*guest, error) {
	if s.devLXD == devLXDOff {
		return nil, nil
	}
	c, err := devlxd.Connect(ctx, devlxd.Config{Socket: s.socket})
	if err != nil {
		if s.devLXD == devLXDRequired {
			return nil, fmt.Errorf("devLXD is required: %w", err)
		}
		slog.Debug("devLXD not available", "socket", s.socket, "error", err.Error())
		return nil, nil
	}
	return &guest{c: c}, nil
}

// instanceType reads GET /1.0's instance_type.
func (g *guest) instanceType(ctx context.Context) (lxdinstancev1.InstanceType, error) {
	resp, err := g.c.Get(ctx, "/1.0")
	if err != nil {
		return 0, err
	}
	var info struct {
		InstanceType string `json:"instance_type"`
	}
	if err := resp.Decode(&info); err != nil {
		return 0, err
	}
	switch info.InstanceType {
	case "container":
		return lxdinstancev1.InstanceType_INSTANCE_TYPE_CONTAINER, nil
	case "virtual-machine":
		return lxdinstancev1.InstanceType_INSTANCE_TYPE_VIRTUAL_MACHINE, nil
	}
	return 0, fmt.Errorf("devLXD reports instance type %q", info.InstanceType)
}

// MetaData reads GET /1.0/meta-data, text in devLXD's plain format and a
// string in an envelope.
func (g *guest) MetaData(ctx context.Context) (claim.MetaData, error) {
	resp, err := g.c.Get(ctx, "/1.0/meta-data")
	if err != nil {
		return claim.MetaData{}, err
	}
	text := string(resp.Body)
	switch {
	case resp.Format == devlxd.FormatEnvelope:
		err = resp.Decode(&text)
	case lxd.StatusKind(resp.Status) != lxd.KindNone:
		err = &lxd.Error{Kind: lxd.StatusKind(resp.Status), Status: resp.Status, Err: errors.New(text)}
	}
	if err != nil {
		return claim.MetaData{}, err
	}
	return claim.ParseMetaData(text)
}
